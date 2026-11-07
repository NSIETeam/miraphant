import React, { useCallback, useEffect, useRef, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { StrictAPI as API } from '../../helpers';

const dateTime = (value) => value ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : '—';
const shanghaiYesterday = () => {
  const parts = new Intl.DateTimeFormat('en-CA', { timeZone: 'Asia/Shanghai', year: 'numeric', month: '2-digit', day: '2-digit' }).formatToParts(new Date());
  const values = Object.fromEntries(parts.map(({ type, value }) => [type, value]));
  return new Date(Date.UTC(Number(values.year), Number(values.month) - 1, Number(values.day)) - 86400000).toISOString().slice(0, 10);
};
const money = (fen = 0) => `¥${(Number(fen || 0) / 100).toFixed(2)}`;
const providers = { wechat: '微信支付', alipay: '支付宝' };
const findings = {
  matched: '已匹配', other_scope: '其他商户或应用', missing_local: '未找到本站记录',
  missing_provider: '渠道账单缺少记录', duplicate: '重复明细', identity_mismatch: '身份不一致',
  amount_mismatch: '金额不一致', state_mismatch: '状态不一致', historical_processing: '历史处理中记录',
};
const outcomes = { imported: '已导入', replayed: '已读取既有批次', unsupported: '已保存，格式暂不支持', failed: '导入失败', started: '处理中或结果尚未确认' };
const importErrorCodes = {
  source_key_unconfigured: '来源加密配置未就绪，账单未能安全保存。',
  provider_unconfigured: '该支付渠道尚未配置。',
  import_busy: '已有账单导入正在处理，请稍后查看记录。',
  timeout: '获取或核验账单超时，账单未确认导入。',
  download_or_import_failed: '渠道账单获取或核验失败，账单未导入。',
};
const errorText = (error, fallback) => {
  const status = error?.response?.status;
  if (status === 401) return '登录状态已失效，请重新登录。';
  if (status === 403) {
    const serverMessage = String(error?.response?.data?.error || '').toLowerCase();
    return serverMessage.includes('csrf') || serverMessage.includes('安全验证')
      ? '安全验证已失效，请刷新页面后重试。'
      : '当前账户没有此项对账权限，或权限已被撤销。';
  }
  if (status === 404) return '没有找到这条账单记录。';
  if (status === 409) return '记录状态已变化，或业务编号已用于其他内容。请刷新后核对。';
  if (status === 429) return '导入请求较频繁，请稍后再试。';
  if (status === 503) return '对账数据或导入配置暂不可用；已有批次仍可查看。';
  if (status === 400) return '筛选或提交内容不符合要求，请检查后再试。';
  return fallback;
};
const queryString = (filters, before = '') => {
  const params = new URLSearchParams({ limit: '30' });
  Object.entries(filters).forEach(([key, value]) => { if (value) params.set(key, value); });
  if (before) params.set('before_id', before);
  return params.toString();
};
const stableKey = (scope, memory) => {
  const storageKey = `miraphant.reconciliation.pending.${scope}`;
  if (memory?.[storageKey]?.key && memory?.[storageKey]?.payload) return { storageKey, ...memory[storageKey] };
  try {
    const current = sessionStorage.getItem(storageKey);
    if (current) {
      try {
        const saved = JSON.parse(current);
        if (saved?.key && saved?.payload) {
          if (memory) memory[storageKey] = saved;
          return { storageKey, key: saved.key, payload: saved.payload };
        }
        return { storageKey, key: current, payload: null };
      } catch (_) { return { storageKey, key: current, payload: null }; }
    }
  } catch (_) { /* Continue with a key for this page lifetime. */ }
  const key = `recon-${window.crypto?.randomUUID ? window.crypto.randomUUID() : `${Date.now()}-${Math.random().toString(36).slice(2)}`}`;
  return { storageKey, key, payload: null };
};
const clearKey = (storageKey, memory) => { if (memory) delete memory[storageKey]; try { sessionStorage.removeItem(storageKey); } catch (_) { /* no-op */ } };
const savePendingAction = (storageKey, key, payload, memory) => {
  const saved = { key, payload };
  if (memory) memory[storageKey] = saved;
  try { sessionStorage.setItem(storageKey, JSON.stringify(saved)); } catch (_) { /* In-memory retry remains available. */ }
};

function Alert({ kind = 'info', children }) { return <div className={`platform-alert platform-alert-${kind}`} role={kind === 'error' ? 'alert' : 'status'}>{children}</div>; }
function Empty({ title, detail }) { return <div className='platform-empty'><strong>{title}</strong><span>{detail}</span></div>; }
function Failure({ error, retry }) { return <Alert kind='error'><strong>暂时无法显示</strong><span>{errorText(error, '记录暂时无法读取，请稍后重试。')}</span>{retry && <button className='platform-button secondary' onClick={retry}>重试</button>}</Alert>; }
function PageTitle({ eyebrow, title, intro, action }) { return <div className='platform-title-row'><div><div className='platform-eyebrow'>{eyebrow}</div><h1>{title}</h1><p>{intro}</p></div>{action}</div>; }
function PageMore({ cursor, loading, onClick }) { return cursor ? <div className='admin-page-footer'><button className='platform-button secondary' disabled={loading} onClick={onClick}>{loading ? '正在加载…' : '加载更早记录'}</button></div> : null; }
function redirectExpired(error, navigate) {
  if (error?.response?.status !== 401) return;
  localStorage.removeItem('user');
  const from = `${window.location.pathname}${window.location.search}`;
  navigate('/login', { replace: true, state: { from } });
}

export function AdminReconciliationPage({ capabilities: grantedCapabilities = [] }) {
  const { batchKey = '' } = useParams();
  const navigate = useNavigate();
  const canImport = grantedCapabilities.includes('reconciliation.import');
  const canNote = grantedCapabilities.includes('reconciliation.note');
  const [status, setStatus] = useState(null);
  const [statusError, setStatusError] = useState(null);
  const [draft, setDraft] = useState({ provider: '', bill_date: '', status: '' });
  const [filters, setFilters] = useState({});
  const [batches, setBatches] = useState([]);
  const [batchCursor, setBatchCursor] = useState('');
  const [batchLoading, setBatchLoading] = useState(true);
  const [batchMoreLoading, setBatchMoreLoading] = useState(false);
  const [batchError, setBatchError] = useState(null);
  const [attempts, setAttempts] = useState([]);
  const [attemptCursor, setAttemptCursor] = useState('');
  const [attemptError, setAttemptError] = useState(null);
  const [detail, setDetail] = useState(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [detailError, setDetailError] = useState(null);
  const [rows, setRows] = useState([]);
  const [rowsCursor, setRowsCursor] = useState('');
  const [rowsLoading, setRowsLoading] = useState(false);
  const [rowsError, setRowsError] = useState(null);
  const [differences, setDifferences] = useState([]);
  const [differenceCursor, setDifferenceCursor] = useState('');
  const [differenceLoading, setDifferenceLoading] = useState(false);
  const [differenceError, setDifferenceError] = useState(null);
  const [differenceFilters, setDifferenceFilters] = useState({ classification: '', severity: '' });
  const [expandedDifference, setExpandedDifference] = useState(0);
  const [actionsByDifference, setActionsByDifference] = useState({});
  const [actionsCursor, setActionsCursor] = useState({});
  const [actionBusy, setActionBusy] = useState(0);
  const [actionError, setActionError] = useState('');
  const [actionNotice, setActionNotice] = useState('');
  const [actionDrafts, setActionDrafts] = useState({});
  const [importOpen, setImportOpen] = useState(false);
  const [importProvider, setImportProvider] = useState('wechat');
  const [importDate, setImportDate] = useState('');
  const [importBusy, setImportBusy] = useState(false);
  const [importError, setImportError] = useState('');
  const [importNotice, setImportNotice] = useState('');
  const listSeq = useRef(0);
  const attemptSeq = useRef(0);
  const rowSeq = useRef(0);
  const diffSeq = useRef(0);
  const detailSeq = useRef(0);
  const pendingActionMemory = useRef({});

  const loadStatus = useCallback(async () => {
    setStatusError(null);
    try {
      const response = await API.get('/api/admin/reconciliation/status');
      if (!Array.isArray(response?.data?.providers)) throw new Error('invalid status');
      setStatus(response.data);
    } catch (error) { redirectExpired(error, navigate); setStatusError(error); }
  }, [navigate]);

  const loadBatches = useCallback(async (before = '', append = false, applied = filters) => {
    const seq = ++listSeq.current;
    append ? setBatchMoreLoading(true) : setBatchLoading(true);
    setBatchError(null);
    try {
      const response = await API.get(`/api/admin/reconciliation/batches?${queryString(applied, before)}`);
      if (seq !== listSeq.current) return;
      if (!Array.isArray(response?.data?.batches) || typeof response?.data?.next_cursor !== 'string') throw new Error('invalid batches');
      setBatches((current) => append ? [...current, ...response.data.batches] : response.data.batches);
      setBatchCursor(response.data.next_cursor);
    } catch (error) {
      redirectExpired(error, navigate);
      if (seq === listSeq.current) { setBatchError(error); if (!append) { setBatches([]); setBatchCursor(''); } }
    } finally { if (seq === listSeq.current) { setBatchLoading(false); setBatchMoreLoading(false); } }
  }, [filters, navigate]);

  const loadAttempts = useCallback(async (before = '', append = false) => {
    const seq = ++attemptSeq.current;
    setAttemptError(null);
    try {
      const response = await API.get(`/api/admin/reconciliation/import-attempts?${queryString({}, before)}`);
      if (seq !== attemptSeq.current) return;
      if (!Array.isArray(response?.data?.attempts) || typeof response?.data?.next_cursor !== 'string') throw new Error('invalid attempts');
      setAttempts((current) => append ? [...current, ...response.data.attempts] : response.data.attempts);
      setAttemptCursor(response.data.next_cursor);
      return response.data.attempts;
    } catch (error) { if (seq === attemptSeq.current) { redirectExpired(error, navigate); setAttemptError(error); if (!append) setAttempts([]); } return null; }
  }, [navigate]);

  const loadDetail = useCallback(async (key) => {
    if (!key) { setDetail(null); setRows([]); setDifferences([]); return; }
    const seq = ++detailSeq.current;
    setDetailLoading(true); setDetailError(null); setDetail(null); setRows([]); setDifferences([]);
    setRowsCursor(''); setDifferenceCursor(''); setActionsByDifference({}); setActionsCursor({}); setExpandedDifference(0);
    try {
      const response = await API.get(`/api/admin/reconciliation/batches/${encodeURIComponent(key)}`);
      if (seq !== detailSeq.current) return;
      if (!response?.data?.batch) throw new Error('invalid batch detail');
      setDetail(response.data.batch);
    } catch (error) { if (seq === detailSeq.current) { redirectExpired(error, navigate); setDetailError(error); } }
    finally { if (seq === detailSeq.current) setDetailLoading(false); }
  }, [navigate]);

  const loadRows = useCallback(async (key, before = '', append = false) => {
    const seq = ++rowSeq.current;
    setRowsLoading(true); setRowsError(null);
    try {
      const response = await API.get(`/api/admin/reconciliation/batches/${encodeURIComponent(key)}/rows?${queryString({}, before)}`);
      if (seq !== rowSeq.current) return;
      if (!Array.isArray(response?.data?.rows) || typeof response?.data?.next_cursor !== 'string') throw new Error('invalid rows');
      setRows((current) => append ? [...current, ...response.data.rows] : response.data.rows);
      setRowsCursor(response.data.next_cursor);
    } catch (error) { if (seq === rowSeq.current) { redirectExpired(error, navigate); setRowsError(error); if (!append) setRows([]); } }
    finally { if (seq === rowSeq.current) setRowsLoading(false); }
  }, [navigate]);

  const loadDifferences = useCallback(async (key, before = '', append = false, applied = differenceFilters) => {
    const seq = ++diffSeq.current;
    setDifferenceLoading(true); setDifferenceError(null);
    try {
      const response = await API.get(`/api/admin/reconciliation/batches/${encodeURIComponent(key)}/differences?${queryString(applied, before)}`);
      if (seq !== diffSeq.current) return;
      if (!Array.isArray(response?.data?.differences) || typeof response?.data?.next_cursor !== 'string') throw new Error('invalid differences');
      setDifferences((current) => append ? [...current, ...response.data.differences] : response.data.differences);
      setDifferenceCursor(response.data.next_cursor);
    } catch (error) { if (seq === diffSeq.current) { redirectExpired(error, navigate); setDifferenceError(error); if (!append) setDifferences([]); } }
    finally { if (seq === diffSeq.current) setDifferenceLoading(false); }
  }, [differenceFilters, navigate]);

  useEffect(() => { loadStatus(); loadBatches(); loadAttempts(); }, [loadStatus, loadBatches, loadAttempts]);
  useEffect(() => { rowSeq.current++; diffSeq.current++; loadDetail(batchKey); }, [batchKey, loadDetail]);
  useEffect(() => { if (batchKey && detail) { loadRows(batchKey); loadDifferences(batchKey); } }, [batchKey, detail, loadRows, loadDifferences]);

  const providerStatuses = status?.providers || [];
  const statusFor = (provider) => providerStatuses.find((item) => item.provider === provider);
  const applyFilters = (event) => { event.preventDefault(); listSeq.current++; setBatches([]); setBatchCursor(''); setFilters({ ...draft }); };
  const clearFilters = () => { listSeq.current++; setDraft({ provider: '', bill_date: '', status: '' }); setBatches([]); setBatchCursor(''); setFilters({}); };
  const openBatch = (key) => navigate(`/admin/reconciliation/${encodeURIComponent(key)}`);

  const openActions = async (differenceID) => {
    setExpandedDifference((current) => current === differenceID ? 0 : differenceID);
    const saved = stableKey(`${userScope()}.${differenceID}`, pendingActionMemory.current);
    if (saved.payload) setActionDrafts((current) => ({ ...current, [differenceID]: { ...saved.payload, _pending: true } }));
    if (actionsByDifference[differenceID]) return;
    try {
      const response = await API.get(`/api/admin/reconciliation/differences/${differenceID}/actions?limit=30`);
      setActionsByDifference((current) => ({ ...current, [differenceID]: response?.data?.actions || [] }));
      setActionsCursor((current) => ({ ...current, [differenceID]: response?.data?.next_cursor || '' }));
    } catch (error) { redirectExpired(error, navigate); setActionError(errorText(error, '处理记录暂时无法读取。')); }
  };

  const submitAction = async (difference) => {
    const draftAction = actionDrafts[difference.id] || { action: 'note', reason: '', business_ref: '' };
    if (!draftAction.reason.trim() || (draftAction.action !== 'note' && !draftAction.business_ref.trim())) {
      setActionError('请填写处理说明；记录查询或外部引用时还需填写业务编号。'); return;
    }
    const scope = `${userScope()}.${difference.id}`;
    const saved = stableKey(scope, pendingActionMemory.current);
    const hadPending = Boolean(saved.payload);
    const key = saved.key;
    const payload = saved.payload || { action_key: key, action: draftAction.action, reason: draftAction.reason.trim(), business_ref: draftAction.business_ref.trim() };
    setActionBusy(difference.id); setActionError(''); setActionNotice('');
    let requestSent = false;
    try {
      const csrfResponse = await API.get('/api/refund-auth/csrf');
      const csrf = csrfResponse?.data?.csrf_token;
      if (!csrf) throw new Error('csrf unavailable');
      savePendingAction(saved.storageKey, key, payload, pendingActionMemory.current);
      setActionDrafts((current) => ({ ...current, [difference.id]: { ...payload, _pending: true } }));
      requestSent = true;
      const response = await API.post(`/api/admin/reconciliation/differences/${difference.id}/actions`, payload, { headers: { 'X-CSRF-Token': csrf } });
      if (!response?.data?.action) throw new Error('action response unavailable');
      clearKey(saved.storageKey, pendingActionMemory.current);
      setActionsByDifference((current) => ({ ...current, [difference.id]: [response.data.action, ...(current[difference.id] || []).filter((item) => item.id !== response.data.action.id)] }));
      setActionNotice('处理记录已追加。差异仍需通过已有支付核实流程处理。');
      setActionDrafts((current) => ({ ...current, [difference.id]: { action: 'note', reason: '', business_ref: '' } }));
    } catch (error) {
      redirectExpired(error, navigate);
      if (!hadPending && [400, 401, 403].includes(error?.response?.status)) {
        clearKey(saved.storageKey, pendingActionMemory.current);
        setActionDrafts((current) => ({ ...current, [difference.id]: { ...payload, _pending: false } }));
      }
      setActionError(error?.response?.status === 409
        ? '这个业务编号已经对应其他处理内容。系统没有更换编号；请刷新处理记录并核对。'
        : errorText(error, requestSent ? '提交结果暂未确认。请保持本次内容不变后重试，系统会使用同一业务编号。' : '安全验证尚未完成，请重新登录或刷新权限后重试。'));
    } finally { setActionBusy(0); }
  };

  const importBill = async (event) => {
    event.preventDefault();
    if (!importDate || !canImport) return;
    setImportBusy(true); setImportError(''); setImportNotice('账单正在获取和核验，页面关闭后可在导入记录中查看结果。');
    try {
      const csrfResponse = await API.get('/api/refund-auth/csrf');
      const csrf = csrfResponse?.data?.csrf_token;
      if (!csrf) throw new Error('csrf unavailable');
      const response = await API.post('/api/admin/reconciliation/import', { provider: importProvider, bill_date: importDate }, { headers: { 'X-CSRF-Token': csrf } });
      const batch = response?.data?.batch;
      if (!batch) throw new Error('import response unavailable');
      setImportNotice(response.data.created ? '账单已导入并生成固定核对记录。' : '找到相同来源的既有批次，已返回原记录。');
      setImportOpen(false);
      setImportDate('');
      await Promise.all([loadBatches(), loadAttempts()]);
      openBatch(batch.batch_key);
    } catch (error) {
      redirectExpired(error, navigate);
      if (error?.response) setImportNotice('');
      const refreshedAttempts = await loadAttempts();
      const responseData = error?.response?.data || {};
      const requestKey = responseData.request_key;
      const linkedAttempt = requestKey
        ? groupAttempts(refreshedAttempts || []).find((attempt) => attempt.request_key === requestKey)
        : null;
      const errorCode = responseData.error_code || linkedAttempt?.error_code;
      if (responseData.outcome === 'failed' || linkedAttempt?.outcome === 'failed') {
        setImportError(importErrorCodes[errorCode] || '账单导入失败，未生成可核对批次。');
      } else if (responseData.outcome === 'unknown' || linkedAttempt?.phase === 'started' || !error?.response) {
        setImportError('请求结果暂未确认。请查看导入记录，稍后再核实。');
      } else {
        setImportError(errorText(error, '请求未能完成，请检查输入或稍后重试。'));
      }
    } finally { setImportBusy(false); }
  };

  const content = batchKey ? <BatchDetail
    batchKey={batchKey} detail={detail} loading={detailLoading} error={detailError} retry={() => loadDetail(batchKey)}
    rows={rows} rowsCursor={rowsCursor} rowsLoading={rowsLoading} rowsError={rowsError} loadRows={loadRows}
    differences={differences} differenceCursor={differenceCursor} differenceLoading={differenceLoading} differenceError={differenceError}
    loadDifferences={loadDifferences} differenceFilters={differenceFilters} setDifferenceFilters={setDifferenceFilters}
    expandedDifference={expandedDifference} openActions={openActions} actionsByDifference={actionsByDifference}
    actionsCursor={actionsCursor} setActionsByDifference={setActionsByDifference} setActionsCursor={setActionsCursor} setActionError={setActionError}
    navigate={navigate}
    canNote={canNote} actionDrafts={actionDrafts} setActionDrafts={setActionDrafts}
    submitAction={submitAction} actionBusy={actionBusy} actionError={actionError} actionNotice={actionNotice}
    goBack={() => navigate('/admin/reconciliation')}
  /> : <>
    <section className='platform-panel recon-status-panel'>
      <div className='platform-panel-head'><div><div className='platform-eyebrow'>IMPORT READINESS</div><h2>导入准备情况</h2></div><button className='platform-button secondary' onClick={loadStatus}>刷新状态</button></div>
      {statusError ? <Failure error={statusError} retry={loadStatus} /> : !status ? <div className='platform-state'>正在读取渠道状态…</div> : <>
        <div className='recon-provider-grid'>{providerStatuses.map((provider) => <article key={provider.provider}><div><strong>{providers[provider.provider] || provider.provider}</strong><span className={`recon-status-chip ${provider.configured ? 'ready' : 'off'}`}>{provider.configured ? '商户已配置' : '尚未配置'}</span></div><p>{provider.supported ? '微信 ALL 交易账单可解析并核对。' : '目前可保存来源证据，但格式尚不支持逐行解析。'}</p></article>)}</div>
        <p className='recon-readiness-note'>{status.source_encryption_ready ? '账单来源加密已就绪。' : '来源加密配置未就绪；已有记录仍可查看，暂不能导入新账单。'} 导入结果可能是处理中、失败或格式暂不支持；只有批次证据完整后才会显示固定核对分类。</p>
      </>}
    </section>
    {importNotice && <Alert kind='success'>{importNotice}</Alert>}
    <section className='platform-panel'>
      <div className='platform-panel-head'><div><div className='platform-eyebrow'>IMMUTABLE BATCHES</div><h2>账单批次</h2></div><span className='admin-result-count'>{batches.length ? `已加载 ${batches.length} 个批次` : ''}</span></div>
      <form className='admin-filter-form recon-filter-form' onSubmit={applyFilters}>
        <label>支付渠道<select value={draft.provider} onChange={(e) => setDraft((old) => ({ ...old, provider: e.target.value }))}><option value=''>全部渠道</option><option value='wechat'>微信支付</option><option value='alipay'>支付宝</option></select></label>
        <label>账单日期<input type='date' value={draft.bill_date} onChange={(e) => setDraft((old) => ({ ...old, bill_date: e.target.value }))} /></label>
        <label>处理状态<select value={draft.status} onChange={(e) => setDraft((old) => ({ ...old, status: e.target.value }))}><option value=''>全部状态</option><option value='imported'>已解析</option><option value='unsupported_format'>格式暂不支持</option></select></label>
        <div className='admin-filter-footer'><small>批次和核对差异为固定记录；新版本不会覆盖旧证据。</small><div><button type='button' className='platform-button secondary' onClick={clearFilters}>清空</button><button className='platform-button primary'>应用筛选</button></div></div>
      </form>
      {batchLoading ? <div className='platform-state'>正在读取账单批次…</div> : batchError ? <Failure error={batchError} retry={() => loadBatches()} /> : !batches.length ? <Empty title='还没有账单批次' detail='具备导入权限的财务人员可从支付平台获取已完成日期的交易账单。' /> : <>
        <div className='recon-batch-list'>{batches.map((batch) => <button className='recon-batch-card' key={batch.batch_key} onClick={() => openBatch(batch.batch_key)}><span className='recon-batch-top'><strong>{providers[batch.provider] || batch.provider} · {batch.bill_date}</strong><span className={`recon-status-chip ${batch.status === 'imported' ? 'ready' : 'warning'}`}>{batch.status === 'imported' ? '已解析' : '格式暂不支持'}</span></span><span className='recon-batch-meta'>{batch.bill_type} · {batch.format_version} · 版本 {batch.import_version}</span><span className='recon-batch-meta'>{batch.row_count} 行 · 导入于 {dateTime(batch.created_at)}</span><code>来源摘要 {batch.source_sha256}</code><span className='recon-batch-open'>查看批次 →</span></button>)}</div>
        <PageMore cursor={batchCursor} loading={batchMoreLoading} onClick={() => loadBatches(batchCursor, true)} />
      </>}
    </section>
    <section className='platform-panel'>
      <div className='platform-panel-head'><div><div className='platform-eyebrow'>IMPORT ATTEMPTS</div><h2>导入记录</h2></div><button className='platform-button secondary' onClick={() => loadAttempts()}>刷新</button></div>
      <p className='recon-muted'>只有 started 的记录表示请求已受理、结果尚未确认，不代表导入成功或失败。</p>
      {attemptError ? <Failure error={attemptError} retry={() => loadAttempts()} /> : !attempts.length ? <Empty title='暂无导入请求' detail='提交导入后，请在这里查看处理结果。' /> : <div className='recon-attempt-list'>{groupAttempts(attempts).map((attempt) => <article key={attempt.request_key}><div><strong>{providers[attempt.provider] || attempt.provider} · {attempt.bill_date}</strong><span className={`recon-status-chip ${attempt.outcome === 'failed' ? 'off' : attempt.phase === 'started' || attempt.outcome === 'unsupported' ? 'warning' : 'ready'}`}>{outcomes[attempt.outcome] || outcomes.started}</span></div><small>{dateTime(attempt.created_at)} · 操作人 #{attempt.actor_user_id} · 请求编号 {attempt.request_key.slice(0, 12)}</small>{attempt.outcome === 'failed' && attempt.error_code && <p className='recon-muted'>{importErrorCodes[attempt.error_code] || '账单导入失败，未生成可核对批次。'}</p>}{attempt.source_sha256 && <code>来源摘要 {attempt.source_sha256}</code>}</article>)}</div>}
      <PageMore cursor={attemptCursor} loading={false} onClick={() => loadAttempts(attemptCursor, true)} />
    </section>
    {importOpen && <div className='recon-modal-backdrop' role='presentation' onClick={(event) => { if (event.target === event.currentTarget && !importBusy) setImportOpen(false); }}><section className='recon-modal platform-panel' role='dialog' aria-modal='true' aria-labelledby='recon-import-title'><div className='platform-panel-head'><div><div className='platform-eyebrow'>SERVER-SIDE IMPORT</div><h2 id='recon-import-title'>导入已完成日期的账单</h2></div><button className='platform-button secondary' disabled={importBusy} onClick={() => setImportOpen(false)}>关闭</button></div>
      {!status && <div className='platform-state'>正在读取可用状态…</div>}{statusError && <Failure error={statusError} retry={loadStatus} />}
      {status && !status.source_encryption_ready && <Alert kind='warning'>来源加密配置未就绪，无法安全保存账单来源。请联系平台管理员。</Alert>}
      <form className='platform-form recon-import-form' onSubmit={importBill}><label>支付渠道<select value={importProvider} onChange={(e) => setImportProvider(e.target.value)}><option value='wechat'>微信支付</option><option value='alipay'>支付宝</option></select></label><label>账单日期<input type='date' value={importDate} max={shanghaiYesterday()} onChange={(e) => setImportDate(e.target.value)} required /></label>
        {status && <div className='form-wide recon-import-provider-note'>{(() => { const provider = statusFor(importProvider); if (!provider?.configured) return '该渠道尚未配置商户，暂时不能导入。'; if (!status.source_encryption_ready) return '加密尚未就绪，暂时不能导入。'; if (!provider.supported) return '该渠道的账单格式目前不支持逐行解析。若导入，系统会单独保存为“格式暂不支持”，不会显示为已核对。'; return '只会从服务器配置的商户获取账单，不会提交其他商户信息。相同来源会返回既有批次。'; })()}</div>}
        {importError && <div className='form-wide'><Alert kind='error'>{importError}</Alert></div>}{importNotice && <div className='form-wide'><Alert>{importNotice}</Alert></div>}
        <div className='form-wide platform-form-footer'><span>仅可导入上海时区已完成日期的账单；导入不会改变积分。</span><button className='platform-button primary' disabled={importBusy || !canImport || !importDate || !statusFor(importProvider)?.configured || !status?.source_encryption_ready}>{importBusy ? '正在导入…' : '从支付渠道获取'}</button></div>
      </form>
    </section></div>}
  </>;

  return <main className='platform-page admin-reconciliation-page'>
    <PageTitle eyebrow='ADMIN · RECONCILIATION' title='支付对账' intro='查看账单核对证据与导入记录；只有获授权的操作会显示。' action={batchKey ? <button className='platform-button secondary' onClick={() => navigate('/admin/reconciliation')}>返回批次</button> : canImport ? <button className='platform-button primary' onClick={() => { setImportOpen(true); setImportError(''); setImportNotice(''); }}>导入账单</button> : null} />
    {content}
  </main>;
}

function userScope() { try { return String(JSON.parse(localStorage.getItem('user') || 'null')?.id || 'anonymous'); } catch (_) { return 'anonymous'; } }
function groupAttempts(attempts) {
  const groups = new Map();
  attempts.forEach((attempt) => {
    const current = groups.get(attempt.request_key) || {};
    if (attempt.phase === 'result') { current.phase = 'result'; current.outcome = attempt.outcome; current.error_code = attempt.error_code; current.created_at = attempt.created_at; current.source_sha256 = attempt.source_sha256; current.batch_id = attempt.batch_id; }
    else if (!current.phase) { current.phase = 'started'; current.outcome = 'started'; current.created_at = attempt.created_at; }
    Object.assign(current, { id: attempt.id, request_key: attempt.request_key, provider: attempt.provider, bill_date: attempt.bill_date, actor_user_id: attempt.actor_user_id });
    groups.set(attempt.request_key, current);
  });
  return Array.from(groups.values()).sort((a, b) => new Date(b.created_at) - new Date(a.created_at));
}

function BatchDetail(props) {
  const batch = props.detail;
  const [tab, setTab] = useState('differences');
  const [draftFilters, setDraftFilters] = useState(props.differenceFilters);
  useEffect(() => { setDraftFilters(props.differenceFilters); }, [props.differenceFilters]);
  return <>
    {props.loading ? <div className='platform-state'>正在读取批次详情…</div> : props.error ? <Failure error={props.error} retry={props.retry} /> : batch && <>
      <section className='platform-panel'><div className='platform-panel-head'><div><div className='platform-eyebrow'>BATCH · {batch.import_version}</div><h2>{providers[batch.provider] || batch.provider} · {batch.bill_date}</h2></div><span className={`recon-status-chip ${batch.status === 'imported' ? 'ready' : 'warning'}`}>{batch.status === 'imported' ? '已解析' : '格式暂不支持'}</span></div>
        <dl className='recon-batch-facts'><div><dt>商户</dt><dd>{batch.merchant_id || '未记录'}</dd></div><div><dt>应用</dt><dd>{batch.app_id || '未记录'}</dd></div><div><dt>账单范围</dt><dd>{batch.bill_type} · {batch.timezone}</dd></div><div><dt>解析格式</dt><dd>{batch.format_version}</dd></div><div><dt>行数</dt><dd>{batch.row_count}</dd></div><div><dt>来源大小</dt><dd>{Number(batch.source_size || 0).toLocaleString()} 字节</dd></div><div><dt>平台校验</dt><dd>{batch.provider_hash_verified ? `已校验 ${batch.provider_hash_type || ''}` : '未提供平台文件摘要'}</dd></div><div className='recon-hash-fact'><dt>来源摘要</dt><dd>{batch.source_sha256}</dd></div></dl>
        {batch.status === 'unsupported_format' && <Alert kind='warning'>该批次只保存了来源摘要和加密原文件，没有可用的逐行解析器；不能视为完成核对。</Alert>}
        <Alert kind='info'>查询、备注或记录外部引用不会自动联系支付渠道，也不会把差异标记为已解决。当前不覆盖退款缺账判断（缺可靠申请时间）。PROCESSING 仅反映账单导出时状态。</Alert>
      </section>
      <section className='platform-panel'><div className='recon-tabs' role='tablist'><button className={tab === 'differences' ? 'active' : ''} onClick={() => setTab('differences')}>核对差异</button><button className={tab === 'rows' ? 'active' : ''} onClick={() => setTab('rows')}>账单行</button></div>
        {tab === 'differences' ? <>
          <form className='admin-filter-form recon-filter-form' onSubmit={(event) => { event.preventDefault(); props.setDifferenceFilters({ ...draftFilters }); }}><label>差异类型<select value={draftFilters.classification} onChange={(e) => setDraftFilters((old) => ({ ...old, classification: e.target.value }))}><option value=''>全部类型</option>{Object.entries(findings).filter(([key]) => key !== 'matched').map(([key, label]) => <option key={key} value={key}>{label}</option>)}</select></label><label>关注程度<select value={draftFilters.severity} onChange={(e) => setDraftFilters((old) => ({ ...old, severity: e.target.value }))}><option value=''>全部</option><option value='attention'>需要关注</option><option value='informational'>信息记录</option></select></label><div className='admin-filter-footer'><small>PROCESSING 是账单当时的状态；后续成功不自动证明历史账单有误。</small><div><button className='platform-button primary'>筛选差异</button></div></div></form>
          {props.differenceLoading && !props.differences.length ? <div className='platform-state'>正在读取差异…</div> : props.differenceError ? <Failure error={props.differenceError} retry={() => props.loadDifferences(props.batchKey)} /> : !props.differences.length ? <Empty title='没有符合筛选条件的差异' detail='这只说明当前批次和筛选条件下没有固定差异记录。' /> : <div className='recon-difference-list'>{props.differences.map((difference) => {
            const actions = props.actionsByDifference[difference.id] || [];
            const draft = props.actionDrafts[difference.id] || { action: 'note', reason: '', business_ref: '' };
            const update = (field, value) => props.setActionDrafts((old) => ({ ...old, [difference.id]: { ...draft, [field]: value } }));
            return <article className='recon-difference-card' key={difference.id}><div className='recon-difference-heading'><span className={`recon-status-chip ${difference.severity === 'attention' ? 'warning' : 'ready'}`}>{findings[difference.classification] || difference.classification}</span><small>{difference.severity === 'attention' ? '需要关注' : '信息记录'} · {dateTime(difference.created_at)}</small></div>
              <dl className='recon-difference-facts'><div><dt>渠道行</dt><dd>{difference.row_id ? `#${difference.row_id}` : '未关联账单行'}</dd></div><div><dt>本地订单</dt><dd>{difference.local_order_id ? `#${difference.local_order_id}` : '未关联'}</dd></div><div><dt>本地退款</dt><dd>{difference.local_refund_id ? `#${difference.local_refund_id}` : '未关联'}</dd></div><div><dt>支付事件</dt><dd>{difference.payment_event_id ? `#${difference.payment_event_id}` : '未关联'}</dd></div><div className='recon-hash-fact'><dt>证据摘要</dt><dd>{difference.evidence_fingerprint}</dd></div></dl>
              <button className='platform-text-button' onClick={() => props.openActions(difference.id)}>{props.expandedDifference === difference.id ? '收起处理记录' : '查看处理记录'}</button>
              {props.expandedDifference === difference.id && <div className='recon-action-area'>
                {props.actionError && <Alert kind='error'>{props.actionError}</Alert>}{props.actionNotice && <Alert kind='success'>{props.actionNotice}</Alert>}
                {actions.length ? <div className='recon-action-list'>{actions.map((action) => <div key={action.id}><strong>{actionLabel(action.action)}</strong><span>{action.reason}</span>{action.business_ref && <small>业务引用：{action.business_ref}</small>}<small>{action.actor_user_id === 0 ? '系统' : `操作人 #${action.actor_user_id}`} · {dateTime(action.created_at)}</small></div>)}</div> : <p className='recon-muted'>尚无处理记录。</p>}
                {props.actionsCursor[difference.id] && <button className='platform-button secondary' onClick={async () => { try { const cursor = props.actionsCursor[difference.id]; const response = await API.get(`/api/admin/reconciliation/differences/${difference.id}/actions?limit=30&before_id=${encodeURIComponent(cursor)}`); props.setActionsByDifference((old) => ({ ...old, [difference.id]: [...(old[difference.id] || []), ...(response?.data?.actions || [])] })); props.setActionsCursor((old) => ({ ...old, [difference.id]: response?.data?.next_cursor || '' })); } catch (error) { redirectExpired(error, props.navigate); props.setActionError(errorText(error, '处理记录暂时无法读取。')); } }}>加载更早处理记录</button>}
                {props.canNote ? <form className='platform-form recon-action-form' onSubmit={(event) => { event.preventDefault(); props.submitAction(difference); }}>
                  {draft._pending && <div className='form-wide'><Alert kind='warning'>上次提交结果尚未确认。请核对并使用原内容重试；系统不会自动更换业务编号。</Alert></div>}
                  <label>记录类型<select disabled={draft._pending} value={draft.action} onChange={(e) => update('action', e.target.value)}><option value='note'>追加备注</option><option value='request_query'>记录查询请求</option><option value='record_reference'>记录外部核实引用</option></select></label>
                  <label className='recon-action-reason'>处理说明<textarea disabled={draft._pending} maxLength={512} rows='3' value={draft.reason} onChange={(e) => update('reason', e.target.value)} required /></label>
                  {draft.action !== 'note' && <label className='recon-action-ref'>业务编号<input disabled={draft._pending} maxLength={180} value={draft.business_ref} onChange={(e) => update('business_ref', e.target.value)} required placeholder='例如已存在的订单或工单编号' /></label>}
                  <div className='form-wide recon-action-disclaimer'>{draft.action === 'request_query' ? '这里只记录查询请求，不会自动向支付渠道发起查询。' : draft.action === 'record_reference' ? '这里只保存你提供的外部业务引用，不证明渠道已核实，也不会关闭差异。' : '备注会作为追加记录保存，不会改变原始差异。'}</div>
                  <div className='form-wide platform-form-footer'><span>未确认的重复提交会使用同一业务编号；若收到冲突提示，请先刷新处理记录。</span><button className='platform-button primary' disabled={props.actionBusy === difference.id || !draft.reason.trim() || (draft.action !== 'note' && !draft.business_ref.trim())}>{props.actionBusy === difference.id ? '正在保存…' : draft._pending ? '重试本次记录' : '追加处理记录'}</button></div>
                </form> : <p className='recon-muted'>当前账户仅可查看处理记录。</p>}
              </div>}
            </article>;
          })}</div>}
          <PageMore cursor={props.differenceCursor} loading={props.differenceLoading} onClick={() => props.loadDifferences(props.batchKey, props.differenceCursor, true)} />
        </> : <>
          {props.rowsLoading && !props.rows.length ? <div className='platform-state'>正在读取账单明细…</div> : props.rowsError ? <Failure error={props.rowsError} retry={() => props.loadRows(props.batchKey)} /> : !props.rows.length ? <Empty title='该批次没有可显示的账单行' detail={batch.status === 'unsupported_format' ? '此格式目前尚未解析。' : '批次中没有规范化明细。'} /> : <>
            <div className='recon-row-list'>{props.rows.map((row) => <article key={row.id}><div className='recon-row-title'><strong>来源第 {row.source_line} 行</strong><span className={`recon-status-chip ${row.result === 'matched' ? 'ready' : row.result === 'historical_processing' ? 'info' : 'warning'}`}>{findings[row.result] || row.result}</span></div><div className='recon-row-main'><span>{row.kind === 'payment' ? '支付' : row.kind === 'refund' ? '退款' : row.kind} · {row.provider_status}{row.refund_status ? ` · ${row.refund_status}` : ''}</span><span>{dateTime(row.occurred_at)}</span></div><div className='recon-row-amounts'><span>订单金额 {money(row.gross_fen)}</span>{row.refund_fen > 0 && <span>退款 {money(row.refund_fen)}</span>}<span>结算 {money(row.settlement_fen)}</span><span>费用 {money(row.fee_fen)}</span>{row.has_net_fen && <span>净额 {money(row.net_fen)}</span>}</div><code>订单/退款编号：{row.order_key || row.merchant_refund_key || '未提供'} · 来源行摘要 {row.source_digest}</code></article>)}</div>
            <PageMore cursor={props.rowsCursor} loading={props.rowsLoading} onClick={() => props.loadRows(props.batchKey, props.rowsCursor, true)} />
          </>}
        </>}
      </section>
    </>}
  </>;
}
function actionLabel(action) { return action === 'note' ? '备注' : action === 'request_query' ? '记录查询请求' : action === 'record_reference' ? '记录外部引用' : action; }
