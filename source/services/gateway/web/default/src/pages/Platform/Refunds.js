import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import { StrictAPI as API } from '../../helpers';

const MICRO = 1000000;
const money = (fen = 0) => `¥${(Number(fen || 0) / 100).toFixed(2)}`;
const points = (micro = 0) => (Number(micro || 0) / MICRO).toLocaleString('zh-CN', { maximumFractionDigits: 6 });
const date = (value) => value ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : '—';
const encode = (value) => encodeURIComponent(String(value || ''));
const stateLabels = {
  awaiting_review: '等待审核', review_approved: '审核通过，等待单独提交', approved: '已确认提交，等待渠道处理',
  submitting: '正在提交', submitted: '渠道处理中', unknown: '状态待核实', needs_manual_review: '需要人工核实',
  succeeded: '退款成功', rejected: '申请未通过', definite_failed: '渠道确认未退款',
};
const stateTone = (state) => ['succeeded'].includes(state) ? 'success' : ['rejected', 'definite_failed'].includes(state) ? 'muted' : ['needs_manual_review', 'unknown', 'review_approved'].includes(state) ? 'warning' : 'pending';
const makeKey = (prefix) => `${prefix}-${window.crypto?.randomUUID ? window.crypto.randomUUID() : `${Date.now()}-${Math.random().toString(36).slice(2)}`}`;
const userID = () => { try { return Number(JSON.parse(localStorage.getItem('user') || 'null')?.id) || 0; } catch { return 0; } };

function PageTitle({ title, intro, action, eyebrow = 'REFUNDS' }) {
  return <div className='platform-title-row'><div><div className='platform-eyebrow'>{eyebrow}</div><h1>{title}</h1><p>{intro}</p></div>{action}</div>;
}
function Alert({ children, kind = 'info' }) { return <div className={`platform-alert platform-alert-${kind}`} role={kind === 'error' ? 'alert' : 'status'}>{children}</div>; }
function APIError({ error, fallback }) {
  const status = error?.response?.status;
  if (status === 401) return '登录状态已失效，请重新登录后继续。';
  if (status === 403) return '当前账户没有这项操作权限。';
  if (status === 404) return '没有找到记录，或记录不属于当前账户。';
  if (status === 409) return '记录状态已变化，请刷新后核对。';
  if (status === 429) return '操作较频繁，请稍后再试。';
  if (status === 503) return '该项服务当前暂停，已有申请仍可查看。';
  return fallback;
}
async function csrf() {
  const response = await API.get('/api/refund-auth/csrf');
  const token = response?.data?.csrf_token;
  if (!token) throw new Error('安全验证暂不可用');
  return token;
}
function StateBadge({ state }) { return <span className={`order-state-chip ${stateTone(state)}`}>{stateLabels[state] || '状态核对中'}</span>; }

export function RefundCapabilityRoute({ anyOf = [], children, moduleLabel = '退款' }) {
  const [state, setState] = useState('loading');
  const [capabilities, setCapabilities] = useState([]);
  const [error, setError] = useState('');
  const navigate = useNavigate();
  const required = anyOf.join(',');
  const load = useCallback(async () => {
    try {
      const response = await API.get('/api/refund-auth/self');
      const data = response?.data;
      if (!data || !Array.isArray(data.capabilities)) throw new Error('permission response unavailable');
      setCapabilities(data.capabilities);
      setState(required.split(',').some((capability) => capability && data.capabilities.includes(capability)) ? 'allowed' : 'denied');
    } catch (e) {
      if (e?.response?.status === 401) {
        localStorage.removeItem('user');
        const path = `${window.location.pathname}${window.location.search}`;
        navigate('/login', { replace: true, state: { from: path } });
      }
      setError(APIError({ error: e, fallback: '暂时无法核验退款权限，请稍后刷新。' }));
      setState('error');
    }
  }, [required, navigate]);
  useEffect(() => { load(); }, [load]);
  if (state === 'loading') return <div className='platform-state'>正在核验当前权限…</div>;
  if (state === 'error') return <main className='platform-page'><PageTitle eyebrow='ACCESS' title={`${moduleLabel}权限核验`} intro='权限会按当前账户状态实时检查。' /><Alert kind='error'>{error}</Alert><button className='platform-button secondary' onClick={load}>重新核验</button></main>;
  if (state === 'denied') return <main className='platform-page'><PageTitle eyebrow='ACCESS' title='无权访问' intro={`此页面仅向已获授权的${moduleLabel}人员开放。`} /><Alert kind='warning'>你的当前账户没有此模块所需的权限。</Alert><Link className='platform-button secondary' to='/console'>返回控制台</Link></main>;
  return typeof children === 'function' ? children(capabilities) : children;
}

function parseYuanToFen(value) {
  const raw = String(value || '').trim();
  if (!/^\d{1,9}(\.\d{1,2})?$/.test(raw)) return null;
  const [yuan, fraction = ''] = raw.split('.');
  const fen = Number(yuan) * 100 + Number((fraction + '00').slice(0, 2));
  return Number.isSafeInteger(fen) && fen > 0 ? fen : null;
}

export function CustomerRefundPanel({ order }) {
  const [quote, setQuote] = useState(null);
  const [rows, setRows] = useState([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [amount, setAmount] = useState('');
  const [reason, setReason] = useState('');
  const [intent, setIntent] = useState(null);
  const [cursor, setCursor] = useState(''); const [hasMore, setHasMore] = useState(false); const [loadingMore, setLoadingMore] = useState(false);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState('');
  const explain = useCallback((e) => APIError({ error: e, fallback: '退款额度或进度暂时无法读取。' }), []);
  const load = useCallback(async (beforeID = '', append = false) => {
    append ? setLoadingMore(true) : setLoading(true); setError('');
    try {
      const orderKey = encode(order.order_key);
      const [quoteResponse, listResponse] = await Promise.all([
        API.get(`/api/payments/orders/${orderKey}/refund-quote`), API.get(`/api/payments/orders/${orderKey}/refunds?limit=25${beforeID ? `&before_id=${encode(beforeID)}` : ''}`),
      ]);
      setQuote(quoteResponse?.data || null);
      const listed = listResponse?.data?.refunds || [];
      setRows((current) => append ? [...current, ...listed] : listed);
      setCursor(listResponse?.data?.next_before_id ? String(listResponse.data.next_before_id) : ''); setHasMore(Boolean(listResponse?.data?.has_more));
      const key = `miraphant.refund-intent.${userID()}.${order.order_key}`;
      let saved = null; try { saved = JSON.parse(sessionStorage.getItem(key) || 'null'); } catch { sessionStorage.removeItem(key); }
      if (saved?.refund_key) {
        const matched = listed.find((row) => row.refund_key === saved.refund_key);
        if (matched && ['succeeded', 'rejected', 'definite_failed'].includes(matched.state)) {
          sessionStorage.removeItem(key); saved = null;
        }
      }
      setIntent(saved);
      if (saved?.state === 'unknown') { setAmount((Number(saved.amount_fen || 0) / 100).toFixed(2)); setReason(saved.reason || ''); }
    } catch (e) { setError(explain(e)); }
    finally { setLoading(false); setLoadingMore(false); }
  }, [order.order_key, explain]);
  useEffect(() => { load(); }, [load]);

  const maxAmount = quote ? (Number(quote.max_refund_fen || 0) / 100).toFixed(2) : '';
  const maxReasonBytes = order.channel === 'wechat' ? 80 : 256;
  const reasonBytes = new TextEncoder().encode(reason.trim()).length;
  const activeRow = rows.find((row) => !['succeeded', 'rejected', 'definite_failed'].includes(row.state));

  const requestRefund = async (event) => {
    event.preventDefault();
    const amountFen = parseYuanToFen(amount);
    if (!amountFen || reasonBytes === 0 || reasonBytes > maxReasonBytes) return;
    setBusy(true); setError(''); setNotice('');
    const storageKey = `miraphant.refund-intent.${userID()}.${order.order_key}`;
    const fingerprint = JSON.stringify({ amount_fen: amountFen, reason: reason.trim() });
    let intent = null;
    try { intent = JSON.parse(sessionStorage.getItem(storageKey) || 'null'); } catch { sessionStorage.removeItem(storageKey); }
    if (intent?.state === 'unknown' && intent.fingerprint !== fingerprint) {
      setError('这笔请求的结果尚未确认。请恢复原金额和原因后重试，避免重复申请。'); setBusy(false); return;
    }
    const idempotencyKey = intent?.key || makeKey('refund-ui');
    const pending = { state: 'unknown', key: idempotencyKey, fingerprint, amount_fen: amountFen, reason: reason.trim() };
    sessionStorage.setItem(storageKey, JSON.stringify(pending));
    try {
      const csrfToken = await csrf();
      setIntent(pending);
      const response = await API.post(`/api/payments/orders/${encode(order.order_key)}/refund-requests`, {
        amount_fen: amountFen, reason: reason.trim(), idempotency_key: idempotencyKey,
      }, { headers: { 'X-CSRF-Token': csrfToken } });
      const known = { ...pending, state: 'known', refund_key: response?.data?.refund_key };
      sessionStorage.setItem(storageKey, JSON.stringify(known)); setIntent(known);
      const state = response?.data?.state;
      setNotice(state === 'succeeded' ? '退款已完成。' : state === 'rejected' || state === 'definite_failed' ? '退款未继续处理，相关冻结积分已释放。' : state === 'awaiting_review' || state === 'review_approved' || state === 'submitted' || state === 'unknown' || state === 'needs_manual_review' ? '退款申请结果已读取，处理进度会显示在下方。' : '退款申请结果已读取。');
      setAmount(''); setReason(''); await load();
    } catch (e) {
      setError(explain(e));
      if ([400, 403, 404, 409, 503].includes(e?.response?.status)) {
        // Keep the key: a concurrent same-key request may have won before this response.
        sessionStorage.setItem(storageKey, JSON.stringify({ ...pending, state: 'unknown' }));
      }
    } finally { setBusy(false); }
  };

  return <section className='platform-panel customer-refund-panel'>
    <div className='platform-panel-head'><div><div className='platform-eyebrow'>REFUND REQUEST</div><h2>退款申请与进度</h2></div><button className='platform-button secondary' onClick={load} disabled={loading}>刷新</button></div>
    {loading ? <div className='platform-state'>正在读取退款额度与进度…</div> : error ? <Alert kind='error'>{error}<button className='platform-button secondary' onClick={load}>重试</button></Alert> : <>
      {quote && <div className='refund-quote-card'><div><span>当前最多可申请</span><strong>{money(quote.max_refund_fen)}</strong></div><div><span>订单金额</span><strong>{money(quote.order_amount_fen)}</strong></div><div><span>已完成退款</span><strong>{money(quote.refunded_fen)}</strong></div><small>这是当前额度估算，提交时平台会重新核验。处理中或待核实的退款会冻结相应积分；赠送积分可能按退款规则同步撤销。</small></div>}
      {quote?.reason && <Alert kind='warning'>{quote.reason}</Alert>}
      {intent?.state === 'unknown' && <Alert kind='warning'><strong>上一笔申请结果待确认</strong><span>表单已恢复原金额和原因。重试会使用同一个请求编号。</span></Alert>}
      {(intent?.state === 'unknown' || (quote?.can_request && !activeRow && !intent?.refund_key)) && <form className='platform-form refund-request-form' onSubmit={requestRefund}>
        <label>{intent?.state === 'unknown' ? '原申请金额（元）' : '申请金额（元）'}<input inputMode='decimal' type='number' min='0.01' max={intent?.state === 'unknown' ? undefined : maxAmount} step='0.01' value={amount} onChange={(e) => setAmount(e.target.value)} placeholder={`最多 ${maxAmount}`} required readOnly={intent?.state === 'unknown'} /></label>
        <label className='refund-reason-field'>申请原因<textarea value={reason} onChange={(e) => setReason(e.target.value)} maxLength='256' rows='3' required placeholder='简要说明退款原因' readOnly={intent?.state === 'unknown'} /><small>{reasonBytes}/{maxReasonBytes} 字节</small></label>
        <div className='form-wide platform-form-footer'><span>{intent?.state === 'unknown' ? '此按钮只重试原申请编号和原金额，不会新建另一笔申请。' : '申请后对应购买积分会被冻结，等待核实期间不可用于模型请求。提交后可在此查看进度。'}</span><button className='platform-button primary' disabled={busy || !parseYuanToFen(amount) || reasonBytes === 0 || reasonBytes > maxReasonBytes}>{busy ? '正在提交…' : intent?.state === 'unknown' ? '核实并重试原申请' : '提交退款申请'}</button></div>
      </form>}
      {notice && <Alert kind='success'>{notice}</Alert>}
      {intent?.refund_key && !activeRow && <div className='platform-alert platform-alert-warning'><strong>最近申请仍待确认</strong><span>当前记录尚未出现在进度列表中。请刷新后核对，不要另建申请。</span><button className='platform-button secondary' onClick={load}>重新核对</button></div>}
      {rows.length === 0 ? <div className='platform-empty'><strong>暂无退款申请</strong><span>{quote?.can_request ? '如需退款，可填写上方金额和原因。' : '此订单目前没有可处理的退款申请。'}</span></div> : <div className='customer-refund-list'>{rows.map((row) => <article className='customer-refund-card' key={row.refund_key}>
        <div className='refund-card-top'><strong>{money(row.amount_fen)}</strong><StateBadge state={row.state} /></div>
        <p>申请时间 {date(row.created_at)} · 渠道 {row.channel === 'wechat' ? '微信支付' : '支付宝'}</p>
        <p>申请原因：{row.reason}</p>
        <p>积分处理：{points(row.purchase_micro)} 购买积分{row.bonus_revoke_micro ? `，另撤销 ${points(row.bonus_revoke_micro)} 赠送积分` : ''}</p>
        {['unknown', 'needs_manual_review', 'submitted', 'submitting', 'approved', 'review_approved'].includes(row.state) && <small>退款仍在处理中或待核实，冻结积分会继续保留。</small>}
      </article>)}</div>}
      {!loading && hasMore && <div className='admin-page-footer'><button className='platform-button secondary' disabled={loadingMore} onClick={() => load(cursor, true)}>{loadingMore ? '正在读取…' : '读取更早退款记录'}</button></div>}
    </>}
  </section>;
}

export function AdminRefundsPage() {
  const [params, setParams] = useSearchParams();
  const [draft, setDraft] = useState({ state: params.get('state') || '', channel: params.get('channel') || '', user_id: params.get('user_id') || '' });
  const [applied, setApplied] = useState({ state: params.get('state') || '', channel: params.get('channel') || '', user_id: params.get('user_id') || '' });
  const [rows, setRows] = useState([]); const [cursor, setCursor] = useState(''); const [hasMore, setHasMore] = useState(false);
  const [loading, setLoading] = useState(true); const [loadingMore, setLoadingMore] = useState(false); const [error, setError] = useState('');
  const seq = useRef(0);
  const filterQuery = useMemo(() => {
    const query = new URLSearchParams();
    Object.entries(applied).forEach(([key, value]) => { if (value.trim()) query.set(key, value.trim()); });
    return query.toString();
  }, [applied]);
  const load = useCallback(async (beforeID = '', append = false, query = filterQuery) => {
    const request = ++seq.current;
    setError(''); append ? setLoadingMore(true) : setLoading(true);
    try {
      const params = new URLSearchParams(query); params.set('limit', '25'); if (beforeID) params.set('before_id', beforeID);
      const response = await API.get(`/api/admin/refunds?${params.toString()}`); const data = response?.data;
      if (!Array.isArray(data?.refunds)) throw new Error('invalid refund list');
      if (request !== seq.current) return;
      setRows((current) => append ? [...current, ...data.refunds] : data.refunds); setCursor(data.next_before_id ? String(data.next_before_id) : ''); setHasMore(Boolean(data.has_more));
    } catch (e) { if (request === seq.current) { setError(APIError({ error: e, fallback: '退款列表暂时无法读取。' })); if (!append) setRows([]); } }
    finally { if (request === seq.current) { setLoading(false); setLoadingMore(false); } }
  }, [filterQuery]);
  useEffect(() => { setRows([]); setCursor(''); load('', false, filterQuery); return () => { seq.current += 1; }; }, [filterQuery, load]);
  const update = (key, value) => setDraft((current) => ({ ...current, [key]: value }));
  const submit = (event) => { event.preventDefault(); setApplied({ ...draft }); setParams(new URLSearchParams(Object.entries(draft).filter(([, value]) => value.trim()))); };
  const clear = () => { const empty = { state: '', channel: '', user_id: '' }; setDraft(empty); setApplied(empty); setParams({}); };
  return <main className='platform-page admin-refund-page'>
    <PageTitle title='退款工作台' eyebrow='FINANCE · REFUNDS' intro='查看客户申请、审核申请，并在单独确认后提交渠道退款。被冻结的积分会在最终结果确认前保留。' action={<Link className='platform-button secondary' to='/console'>返回控制台</Link>} />
    <section className='platform-panel'><form className='admin-filter-form refund-filter-form' onSubmit={submit}>
      <label>处理状态<select value={draft.state} onChange={(e) => update('state', e.target.value)}><option value=''>全部状态</option>{Object.entries(stateLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
      <label>支付渠道<select value={draft.channel} onChange={(e) => update('channel', e.target.value)}><option value=''>全部渠道</option><option value='wechat'>微信支付</option><option value='alipay'>支付宝</option></select></label>
      <label>客户编号<input inputMode='numeric' value={draft.user_id} onChange={(e) => update('user_id', e.target.value)} placeholder='可选' /></label>
      <div className='admin-filter-footer'><span>列表按申请时间倒序；打开记录可查看处理和核实进度。</span><div><button type='button' className='platform-button secondary' onClick={clear}>清空</button><button className='platform-button primary'>应用筛选</button></div></div>
    </form></section>
    <section className='platform-panel'><div className='platform-panel-head'><div><div className='platform-eyebrow'>REFUND REQUESTS</div><h2>申请记录</h2></div></div>
      {loading ? <div className='platform-state'>正在读取退款申请…</div> : error ? <Alert kind='error'>{error}<button className='platform-button secondary' onClick={() => load()}>重试</button></Alert> : rows.length ? <div className='admin-refund-list'>{rows.map((entry) => <Link className='admin-refund-card' key={entry.refund?.refund_key} to={`/admin/refunds/${encode(entry.refund?.refund_key)}`}>
        <div className='refund-card-top'><strong>{money(entry.refund?.amount_fen)}</strong><StateBadge state={entry.refund?.state} /></div><p>客户 #{entry.user_id} · {entry.refund?.channel === 'wechat' ? '微信支付' : '支付宝'} · {date(entry.refund?.created_at)}</p><p>订单 {entry.refund?.order_key}</p><small>{entry.refund?.reason}</small>
      </Link>)}</div> : <div className='platform-empty'><strong>没有符合条件的申请</strong><span>检查筛选条件，或稍后刷新。</span></div>}
      {!loading && !error && hasMore && <div className='admin-page-footer'><button className='platform-button secondary' disabled={loadingMore} onClick={() => load(cursor, true)}>{loadingMore ? '正在读取…' : '加载更早申请'}</button></div>}
    </section>
  </main>;
}

function operationIntent(refundKey, action) {
  const key = `miraphant.refund-decision.${userID()}.${refundKey}.${action}`;
  let saved = null; try { saved = JSON.parse(sessionStorage.getItem(key) || 'null'); } catch { sessionStorage.removeItem(key); }
  if (!saved?.business_key) { saved = { state: 'draft', business_key: makeKey(`refund-${action}`), reason: '' }; sessionStorage.setItem(key, JSON.stringify(saved)); }
  return { key, saved };
}

export function AdminRefundDetailPage() {
  const { refundKey } = useParams();
  const [record, setRecord] = useState(null); const [loading, setLoading] = useState(true); const [error, setError] = useState('');
  const [action, setAction] = useState(''); const [actionLocked, setActionLocked] = useState(false); const [reason, setReason] = useState(''); const [password, setPassword] = useState(''); const [busy, setBusy] = useState(false); const [notice, setNotice] = useState('');
  const [caps, setCaps] = useState([]); const [localPasswordAvailable, setLocalPasswordAvailable] = useState(false); const [operationsEnabled, setOperationsEnabled] = useState(false); const [replayActions, setReplayActions] = useState({});
  const explain = useCallback((e) => APIError({ error: e, fallback: '退款详情暂时无法读取。' }), []);
  const load = useCallback(async () => {
    setError('');
    try { const [detail, self] = await Promise.all([API.get(`/api/admin/refunds/${encode(refundKey)}`), API.get('/api/refund-auth/self')]); setRecord(detail?.data || null); setCaps(self?.data?.capabilities || []); setLocalPasswordAvailable(Boolean(self?.data?.local_password_available)); setOperationsEnabled(Boolean(self?.data?.refund_operations_enabled)); setReplayActions(Object.fromEntries(['approve', 'reject', 'submit'].map((name) => [name, operationIntent(refundKey, name).saved.state === 'sent']))); }
    catch (e) { setRecord(null); setCaps([]); setError(explain(e)); }
    finally { setLoading(false); }
  }, [explain, refundKey]);
  useEffect(() => { setLoading(true); load(); }, [load]);
  const refund = record?.refund;
  const terminal = ['succeeded', 'definite_failed', 'rejected'].includes(refund?.state);
  const refundPointsLabel = refund?.state === 'succeeded' ? '退款扣减购买积分' : ['rejected', 'definite_failed'].includes(refund?.state) ? '已释放购买积分' : '当前冻结购买积分';
  const reviewAllowed = caps.includes('refund.review'); const submitAllowed = caps.includes('refund.submit'); const reconcileAllowed = caps.includes('refund.reconcile');
  const canReview = reviewAllowed && ((operationsEnabled && localPasswordAvailable && refund?.state === 'awaiting_review') || (replayActions.approve && refund?.state === 'review_approved') || (replayActions.reject && refund?.state === 'rejected'));
  const canSubmit = submitAllowed && ((operationsEnabled && localPasswordAvailable && refund?.state === 'review_approved') || (replayActions.submit && ['approved', 'submitting', 'submitted', 'unknown', 'needs_manual_review', 'succeeded', 'definite_failed'].includes(refund?.state)));
  const canReconcile = ['approved', 'submitting', 'submitted', 'unknown', 'needs_manual_review'].includes(refund?.state) && reconcileAllowed;
  const chooseAction = (next) => {
    setAction(next); setNotice(''); setPassword('');
    const intent = operationIntent(refund.refund_key, next); setReason(intent.saved.reason || ''); setActionLocked(intent.saved.state === 'sent');
  };
  const updateReason = (value) => {
    setReason(value);
    if (!refund || !action) return;
    const intent = operationIntent(refund.refund_key, action); if (intent.saved.state !== 'sent') sessionStorage.setItem(intent.key, JSON.stringify({ ...intent.saved, reason: value }));
  };
  const act = async (event) => {
    event.preventDefault(); if (!refund || !action) return;
    const intent = operationIntent(refund.refund_key, action);
    if (intent.saved.state === 'sent' && intent.saved.reason !== reason.trim()) { setError('这项操作已有待确认请求。请恢复原处理原因后重试，避免重复执行。'); return; }
    const bytes = new TextEncoder().encode(reason.trim()).length;
    if (!reason.trim() || bytes > 512 || (!password && intent.saved.state !== 'sent')) { setError('请填写处理原因和本地密码；原因最多 512 字节。'); return; }
    setBusy(true); setError(''); setNotice('');
    const businessKey = intent.saved.business_key;
    try {
      const token = await csrf();
      if (intent.saved.state === 'sent') {
        try {
          await API.post(`/api/admin/refunds/${encode(refund.refund_key)}/${action}`, { business_key: businessKey, reason: reason.trim() }, { headers: { 'X-CSRF-Token': token } });
          sessionStorage.removeItem(intent.key); setAction(''); setActionLocked(false); setPassword(''); setNotice('已读取这项操作的既有结果。'); await load(); return;
        } catch (replayError) {
          if (replayError?.response?.status !== 400) throw replayError;
          if (!password) { setError('尚未找到已记录的操作结果。请重新输入本地密码后再确认。'); return; }
        }
      }
      const ticketResponse = await API.post('/api/refund-auth/step-up', {
        action: `refund.${action}`, refund_key: refund.refund_key, business_key: businessKey, reason: reason.trim(), password,
      }, { headers: { 'X-CSRF-Token': token } });
      const ticket = ticketResponse?.data?.ticket;
      if (!ticket) throw new Error('没有取得操作确认信息');
      sessionStorage.setItem(intent.key, JSON.stringify({ ...intent.saved, state: 'sent', reason: reason.trim() }));
      setActionLocked(true); setReplayActions((current) => ({ ...current, [action]: true }));
      await API.post(`/api/admin/refunds/${encode(refund.refund_key)}/${action}`, {
        business_key: businessKey, reason: reason.trim(), step_up_ticket: ticket,
      }, { headers: { 'X-CSRF-Token': token } });
      sessionStorage.removeItem(intent.key); setAction(''); setActionLocked(false); setPassword('');
      setNotice(action === 'submit' ? '提交意图已保存。退款可能已发送或正在核实，冻结积分会继续保留。' : action === 'approve' ? '审核结果已记录；渠道提交需要单独确认。' : '已记录不通过决定，冻结积分按账本规则处理。');
      await load();
    } catch (e) {
      setPassword(''); setError(APIError({ error: e, fallback: '操作结果暂未确认。保留同一业务编号后可安全重试。' }));
    } finally { setBusy(false); }
  };
  const reconcile = async () => {
    setBusy(true); setError(''); setNotice('');
    try { const token = await csrf(); await API.post(`/api/admin/refunds/${encode(refund.refund_key)}/reconcile`, {}, { headers: { 'X-CSRF-Token': token } }); setNotice('已请求核实。结果未确认前，冻结积分会继续保留。'); await load(); }
    catch (e) { setError(APIError({ error: e, fallback: '暂时无法核实渠道状态，冻结积分仍会保留。' })); }
    finally { setBusy(false); }
  };
  if (loading) return <main className='platform-page'><div className='platform-state'>正在读取退款详情…</div></main>;
  if (error && !record) return <main className='platform-page'><PageTitle title='退款详情' intro='核对申请、审核和渠道进度。' /><Alert kind='error'>{error}<button className='platform-button secondary' onClick={load}>重试</button></Alert><Link className='platform-button secondary' to='/admin/refunds'>返回退款列表</Link></main>;
  if (!refund) return <main className='platform-page'><div className='platform-empty'>退款记录暂不可用。</div></main>;
  return <main className='platform-page admin-refund-detail'>
    <PageTitle eyebrow='REFUND REVIEW' title='退款申请详情' intro='审核通过只表示申请通过；渠道退款需要经过单独确认。渠道结果未确认前，积分会保持冻结。' action={<Link className='platform-button secondary' to='/admin/refunds'>返回退款列表</Link>} />
    {error && <Alert kind='error'>{error}</Alert>}{notice && <Alert kind='success'>{notice}</Alert>}
    <section className='checkout-summary'><div className='checkout-summary-title'><div><StateBadge state={refund.state} /><h2>{money(refund.amount_fen)} · 客户 #{record.user_id}</h2><small>退款编号 {refund.refund_key}</small></div><div className='checkout-price'><span>原充值订单</span><strong>{refund.order_key}</strong></div></div>
      <div className='checkout-facts'><div><span>退款金额</span><strong>{money(refund.amount_fen)}</strong></div><div><span>{refundPointsLabel}</span><strong>{points(refund.purchase_micro)}</strong></div><div><span>赠送积分撤销</span><strong>{points(refund.bonus_revoke_micro)}</strong></div><div><span>支付渠道</span><strong>{refund.channel === 'wechat' ? '微信支付' : '支付宝'}</strong></div><div><span>申请时间</span><strong>{date(refund.created_at)}</strong></div><div><span>最近更新</span><strong>{date(refund.updated_at)}</strong></div></div>
      <p className='refund-reason-display'>客户申请原因：{refund.reason}</p>
      {!terminal && <Alert kind='warning'>此申请仍有 {points(refund.purchase_micro)} 购买积分处于冻结状态。渠道结果待核实期间请勿手动加减余额。</Alert>}
    </section>
    {(!terminal || replayActions.approve || replayActions.reject || replayActions.submit) && <section className='platform-panel refund-action-panel'><div className='platform-panel-head'><div><div className='platform-eyebrow'>AUTHORIZED ACTIONS</div><h2>处理申请</h2></div></div>
      <div className='refund-action-buttons'>{canReview && <>{operationsEnabled && refund.state === 'awaiting_review' && <><button className='platform-button primary' onClick={() => chooseAction('approve')}>审核通过</button><button className='platform-button subtle-danger' onClick={() => chooseAction('reject')}>拒绝申请</button></>}{replayActions.approve && refund.state === 'review_approved' && <button className='platform-button secondary' onClick={() => chooseAction('approve')}>恢复同一审核结果</button>}{replayActions.reject && refund.state === 'rejected' && <button className='platform-button secondary' onClick={() => chooseAction('reject')}>读取同一拒绝结果</button>}</>}{canSubmit && <button className='platform-button primary' onClick={() => chooseAction('submit')}>{replayActions.submit ? '恢复同一提交请求' : '单独确认并提交退款'}</button>}{canReconcile && <button className='platform-button secondary' disabled={busy} onClick={reconcile}>{busy ? '正在核实…' : '核实渠道进度'}</button>}</div>
      {!operationsEnabled && refund.state === 'awaiting_review' && <Alert kind='warning'>新退款审核与提交当前已关闭。历史退款进度仍可核实。</Alert>}
      {!localPasswordAvailable && operationsEnabled && refund.state === 'awaiting_review' && <Alert kind='warning'>此账户没有可用的本地密码，暂不能执行需要重新确认的操作。请联系平台管理员。</Alert>}
      {action && <form className='platform-form refund-stepup-form' onSubmit={act}><div className='refund-stepup-title'><strong>{action === 'approve' ? '审核通过' : action === 'reject' ? '拒绝申请' : '确认提交渠道退款'}</strong><span>请重新输入本地密码确认本次操作；这是一次性确认，不会代替渠道结果核验。</span></div>
        <label>处理原因<textarea value={reason} onChange={(e) => updateReason(e.target.value)} rows='3' maxLength='512' required disabled={actionLocked} /><small>{new TextEncoder().encode(reason.trim()).length}/512 字节</small></label>
        <label>本地密码<input type='password' autoComplete='current-password' value={password} onChange={(e) => setPassword(e.target.value)} required={!actionLocked} /></label>
        <div className='form-wide platform-form-footer'><span>退款编号、金额和当前登录会话都会与确认绑定。</span><button className='platform-button primary' disabled={busy || !reason.trim() || new TextEncoder().encode(reason.trim()).length > 512 || (!password && !actionLocked)}>{busy ? '正在确认…' : actionLocked ? '恢复同一操作' : '重新验证并提交'}</button><button type='button' className='platform-button secondary' onClick={() => setAction('')}>取消</button></div>
      </form>}
    </section>}
    <section className='platform-panel'><div className='platform-panel-head'><div><div className='platform-eyebrow'>DECISIONS</div><h2>处理记录</h2></div></div>{record.decisions?.length ? <div className='refund-timeline'>{record.decisions.map((item, index) => <article key={`${item.business_key}-${index}`}><StateBadge state={item.action === 'approve' ? 'review_approved' : item.action === 'reject' ? 'rejected' : 'approved'} /><strong>{item.action === 'approve' ? '审核通过' : item.action === 'reject' ? '拒绝申请' : item.action === 'submit' ? '已确认提交' : item.action}</strong><span>操作人 #{item.actor_user_id} · {date(item.created_at)}</span><p>{item.reason}</p></article>)}</div> : <div className='platform-empty'><strong>暂无处理记录</strong></div>}</section>
    <section className='platform-panel'><div className='platform-panel-head'><div><div className='platform-eyebrow'>CHANNEL STATUS</div><h2>渠道核实进度</h2></div></div>{record.evidence?.length ? <div className='refund-evidence-list'>{record.evidence.map((item, index) => <article key={`${item.created_at}-${index}`}><StateBadge state={item.outcome === 'succeeded' ? 'succeeded' : item.outcome === 'definite_failed' ? 'definite_failed' : item.outcome === 'pending' ? 'submitted' : 'unknown'} /><span>{date(item.created_at)}</span><strong>{item.outcome === 'succeeded' ? '渠道确认成功' : item.outcome === 'definite_failed' ? '渠道确认未退款' : item.outcome === 'pending' ? '渠道处理中' : '状态待核实'}</strong>{item.amount_fen > 0 && <small>{money(item.amount_fen)}</small>}</article>)}</div> : <div className='platform-empty'><strong>{refund.state === 'rejected' ? '未提交渠道退款' : refund.state === 'definite_failed' ? '渠道确认未退款' : '尚无渠道核实记录'}</strong><span>{refund.state === 'rejected' || refund.state === 'definite_failed' ? '相应冻结积分已释放。' : refund.state === 'awaiting_review' ? '申请尚未提交到支付渠道，相应积分保持冻结。' : refund.state === 'review_approved' ? '审核已通过，尚未单独确认提交到支付渠道；相应积分保持冻结。' : '退款结果尚未确认，相应积分保持冻结。'}</span></div>}</section>
  </main>;
}

export function RefundCapabilityManagementPage() {
  const [targetID, setTargetID] = useState(''); const [grants, setGrants] = useState([]); const [loaded, setLoaded] = useState(false); const [error, setError] = useState('');
  const [selected, setSelected] = useState([]); const [action, setAction] = useState('grant'); const [reason, setReason] = useState(''); const [password, setPassword] = useState(''); const [busy, setBusy] = useState(false); const [notice, setNotice] = useState('');
  const capabilities = [
    ['refund.read', '查看退款申请与进度'], ['refund.review', '审核通过或拒绝'], ['refund.submit', '单独确认并提交退款'],
    ['refund.reconcile', '核实已提交退款进度'], ['refund.audit', '查看退款审计'],
    ['reconciliation.read', '查看账单批次与差异'], ['reconciliation.import', '获取并导入渠道账单'], ['reconciliation.note', '追加对账备注与业务引用'],
  ];
  const load = useCallback(async (id = targetID) => {
    if (!/^\d+$/.test(String(id)) || Number(id) <= 0) { setError('请输入有效的用户编号。'); return; }
    setLoaded(false); setError(''); setNotice('');
    try { const response = await API.get(`/api/admin/refund-auth/users/${encode(id)}/grants`); const current = response?.data?.grants || []; setGrants(current); setSelected(current.filter((grant) => !grant.revoked_at).map((grant) => grant.capability)); setTargetID(String(id)); setLoaded(true); }
    catch (e) { setError(APIError({ error: e, fallback: '授权记录暂时无法读取。' })); setLoaded(false); }
  }, [targetID]);
  const toggle = (capability) => setSelected((items) => items.includes(capability) ? items.filter((item) => item !== capability) : [...items, capability]);
  const submit = async (event) => {
    event.preventDefault();
    if (!Number(targetID) || selected.length === 0 || !reason.trim() || new TextEncoder().encode(reason.trim()).length > 256 || !password) { setError('请填写用户编号、权限、原因和本地密码。'); return; }
    setBusy(true); setError(''); setNotice('');
    try {
      const token = await csrf();
      const ticketResponse = await API.post('/api/refund-auth/step-up', { action: `capability.${action}`, target_user_id: Number(targetID), capabilities: selected, reason: reason.trim(), password }, { headers: { 'X-CSRF-Token': token } });
      const ticket = ticketResponse?.data?.ticket;
      if (!ticket) throw new Error('没有取得操作确认信息');
      await API.post('/api/admin/refund-auth/grants', { action: `capability.${action}`, target_user_id: Number(targetID), capabilities: selected, reason: reason.trim(), step_up_ticket: ticket }, { headers: { 'X-CSRF-Token': token } });
      setPassword(''); setReason(''); await load(targetID); setNotice(action === 'grant' ? '权限已更新。' : '所选权限已撤销。');
    } catch (e) { setPassword(''); setError(APIError({ error: e, fallback: '权限变更结果暂未确认。请刷新目标账户的授权记录后再操作。' })); }
    finally { setBusy(false); }
  };
  return <main className='platform-page refund-capability-page'>
    <PageTitle eyebrow='ROOT · DELEGATED ACCESS' title='财务与对账权限' intro='仅平台管理员可管理委派权限。退款与对账分别授权，不会授予旧版系统管理能力。' action={<Link className='platform-button secondary' to='/admin'>管理首页</Link>} />
    <section className='platform-panel'><form className='platform-form refund-user-lookup' onSubmit={(event) => { event.preventDefault(); load(); }}><label>用户编号<input inputMode='numeric' value={targetID} onChange={(e) => { setTargetID(e.target.value); setLoaded(false); setGrants([]); setSelected([]); setError(''); setNotice(''); }} placeholder='输入账户用户编号' required /></label><button className='platform-button primary'>读取权限</button></form>
      {error && <Alert kind='error'>{error}</Alert>}{loaded && <><div className='refund-grant-list'><h2>历史授权记录</h2><p>记录仅反映授予或撤销时间；账户禁用、角色或登录凭证变更后，权限可能已失效。</p>{grants.length ? grants.map((grant) => <div key={grant.capability}><strong>{capabilities.find(([key]) => key === grant.capability)?.[1] || grant.capability}</strong><span>{grant.revoked_at ? `已撤销 · ${date(grant.revoked_at)}` : `曾授予 · ${date(grant.granted_at)}`}</span></div>) : <div className='platform-empty'><strong>该账户尚无委派权限记录</strong></div>}</div>
        <form className='platform-form refund-grant-form' onSubmit={submit}><div className='refund-form-heading'><h2>更新权限</h2><span>每次变更都需重新输入本地密码，并记录操作原因。</span></div><label>操作<select value={action} onChange={(e) => setAction(e.target.value)}><option value='grant'>授予所选权限</option><option value='revoke'>撤销所选权限</option></select></label><fieldset className='refund-capability-options'><legend>退款处理权限</legend>{capabilities.slice(0, 5).map(([key, label]) => <label key={key}><input type='checkbox' checked={selected.includes(key)} onChange={() => toggle(key)} /><span>{label}</span><small>{key}</small></label>)}</fieldset><fieldset className='refund-capability-options'><legend>账单对账权限</legend>{capabilities.slice(5).map(([key, label]) => <label key={key}><input type='checkbox' checked={selected.includes(key)} onChange={() => toggle(key)} /><span>{label}</span><small>{key}</small></label>)}</fieldset><label>变更原因<textarea value={reason} onChange={(e) => setReason(e.target.value)} rows='3' maxLength='256' required /><small>{new TextEncoder().encode(reason.trim()).length}/256 字节</small></label><label>本地密码<input type='password' autoComplete='current-password' value={password} onChange={(e) => setPassword(e.target.value)} required /></label><div className='platform-form-footer'><span>撤销权限会立即影响该用户后续请求。</span><button className='platform-button primary' disabled={busy || !selected.length || !reason.trim() || new TextEncoder().encode(reason.trim()).length > 256 || !password}>{busy ? '正在更新…' : '重新验证并更新权限'}</button></div></form></>}
    </section>
    {notice && <Alert kind='success'>{notice}</Alert>}
  </main>;
}
