import React, { useEffect, useRef, useState } from 'react';
import { StrictAPI as API, showError, showSuccess } from '../../helpers';

const PAGE_SIZE = 10;
const STORAGE_PREFIX = 'miraphant.admin-points-grant.v1.';
const EMPTY_GRANT = { user_id: '', amount_points: '', business_key: '', reason: '' };

class UserMessageError extends Error {}

function safeErrorMessage(error, fallback) {
  if (error instanceof UserMessageError) return error.message;
  const status = Number(error?.response?.status);
  if (status === 401) return '登录状态已失效。原请求仍保留，请重新登录后用同一请求重试。';
  if (status === 403) return '当前账户未通过权限或安全验证。原请求仍保留，请核对账户后再用同一请求重试。';
  if (status === 409) return '该业务键与已有记录冲突。原请求仍保留，请联系平台支持核对，不要新建另一笔赠送。';
  if (status === 429) return '操作较频繁，请稍后用原请求重试。';
  if (status >= 500) return '服务暂时无法确认结果。原请求仍保留，请稍后用同一请求重试。';
  if (error?.isAxiosError || error?.request) return '网络暂时不可用，服务端结果未确认。原请求仍保留，请稍后用同一请求重试。';
  if (error?.name === 'SecurityError' || error?.name === 'QuotaExceededError') return '浏览器无法访问或保存会话记录，赠送操作已停止。请检查浏览器存储设置。';
  return fallback;
}

function bytes(value) {
  return new TextEncoder().encode(value).length;
}

function validateAmount(value) {
  const match = /^(\d+)(?:\.(\d{1,6}))?$/.exec(value);
  if (!match) return false;
  const digits = `${match[1]}${(match[2] || '').padEnd(6, '0')}`.replace(/^0+(?=\d)/, '');
  if (/^0+$/.test(digits)) return false;
  const max = '9223372036854775807';
  return digits.length < max.length || (digits.length === max.length && digits <= max);
}

function validRequest(request) {
  if (!request || !/^\d+$/.test(String(request.user_id))) return false;
  const userID = Number(request.user_id);
  return Number.isSafeInteger(userID) && userID > 0 &&
    typeof request.amount_points === 'string' && request.amount_points.length <= 32 && validateAmount(request.amount_points) &&
    typeof request.business_key === 'string' && request.business_key.trim() === request.business_key &&
    request.business_key.length > 0 && bytes(request.business_key) <= 149 &&
    typeof request.reason === 'string' && request.reason.trim() === request.reason &&
    request.reason.length > 0 && bytes(request.reason) <= 512;
}

function probeSessionStorage() {
  const storage = window.sessionStorage;
  const key = `${STORAGE_PREFIX}probe`;
  storage.setItem(key, 'ok');
  if (storage.getItem(key) !== 'ok') throw new UserMessageError('浏览器无法可靠保存本次请求，赠送操作已停止。');
  storage.removeItem(key);
  return storage;
}

function readPendingGrant(storage, actorID) {
  const key = `${STORAGE_PREFIX}${actorID}`;
  const raw = storage.getItem(key);
  if (raw === null) return { key, pending: null };
  let parsed;
  try { parsed = JSON.parse(raw); } catch { throw new UserMessageError('恢复记录格式损坏，已停止赠送操作；请保留当前浏览器数据并联系平台支持。'); }
  if (parsed?.version !== 1 || parsed?.state !== 'unknown' || parsed?.actor_id !== actorID || !validRequest(parsed?.request)) {
    throw new UserMessageError('恢复记录不完整或与当前管理员不匹配，已停止赠送操作；请联系平台支持。');
  }
  return { key, pending: parsed };
}

function PageTitle() {
  return <div className='platform-title-row'><div><div className='platform-eyebrow'>ADMIN · CUSTOMERS</div><h1>客户与积分</h1><p>客户列表仅用于识别账户。赠送结果不明时，页面会保留原请求以便安全核实。</p></div></div>;
}

function Alert({ kind = 'error', children }) {
  return <div className={`platform-alert platform-alert-${kind}`} role={kind === 'error' ? 'alert' : 'status'}>{children}</div>;
}

export function AdminUsersPage() {
  const [actor, setActor] = useState(null);
  const [identityState, setIdentityState] = useState('loading');
  const [identityError, setIdentityError] = useState('');
  const [storage, setStorage] = useState(null);
  const [storageError, setStorageError] = useState('');
  const [pending, setPending] = useState(null);
  const [grant, setGrant] = useState(EMPTY_GRANT);
  const [notice, setNotice] = useState('');
  const [submitError, setSubmitError] = useState('');
  const [busy, setBusy] = useState(false);
  const inFlight = useRef(false);

  const [page, setPage] = useState(0);
  const [usersReload, setUsersReload] = useState(0);
  const [users, setUsers] = useState([]);
  const [usersLoading, setUsersLoading] = useState(false);
  const [usersError, setUsersError] = useState('');

  const installIdentity = async () => {
    setIdentityState('loading');
    setIdentityError('');
    try {
      const response = await API.get('/api/user/self');
      const data = response?.data?.data;
      const current = { id: Number(data?.id), role: Number(data?.role), status: Number(data?.status) };
      if (!Number.isSafeInteger(current.id) || current.id <= 0 || current.role < 10 || current.status !== 1) {
        throw new UserMessageError('当前账户没有可用的管理权限。');
      }
      setActor(current);
      setIdentityState('ready');
      try {
        const sessionStorage = probeSessionStorage();
        const restored = readPendingGrant(sessionStorage, current.id);
        setStorage(sessionStorage);
        setPending(restored.pending);
        setGrant(restored.pending ? restored.pending.request : EMPTY_GRANT);
        setStorageError('');
      } catch (error) {
        setStorage(null);
        setStorageError(safeErrorMessage(error, '浏览器会话存储不可用，当前只能查看客户列表。'));
      }
    } catch (error) {
      setActor(null);
      setStorage(null);
      setIdentityState('error');
      setIdentityError(safeErrorMessage(error, '无法核验当前管理员身份。'));
    }
  };

  useEffect(() => { installIdentity(); }, []); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (identityState !== 'ready' || !actor) return undefined;
    let active = true;
    setUsersLoading(true);
    setUsersError('');
    API.get(`/api/user/?p=${page}`).then((response) => {
      if (!response?.data?.success || !Array.isArray(response?.data?.data)) throw new UserMessageError('客户列表响应格式无效');
      if (active) setUsers(response.data.data);
    }).catch(() => {
      if (active) { setUsers([]); setUsersError('客户列表暂时无法读取，请重试。'); }
    }).finally(() => { if (active) setUsersLoading(false); });
    return () => { active = false; };
  }, [actor, identityState, page, usersReload]);

  const update = (key, value) => {
    if (pending || !storage) return;
    setGrant((current) => ({ ...current, [key]: value }));
    setSubmitError('');
    setNotice('');
  };

  const send = async (requestRecord, key) => {
    const check = await API.get('/api/user/self');
    const live = check?.data?.data;
    if (Number(live?.id) !== requestRecord.actor_id || Number(live?.role) < 10 || Number(live?.status) !== 1) {
      throw new UserMessageError('当前管理身份已变化；原请求仍保留，未发送新的赠送操作。');
    }
    const csrf = await API.get('/api/points/csrf');
    const token = csrf?.data?.csrf_token;
    if (!token) throw new UserMessageError('无法取得安全验证信息；原请求已保留，可稍后重试。');
    const response = await API.post('/api/admin/points/adjustments', {
      ...requestRecord.request,
      user_id: Number(requestRecord.request.user_id),
    }, { headers: { 'X-CSRF-Token': token } });
    if (response?.data?.success !== true) throw new UserMessageError('服务端未确认结果；请使用原请求重试。');

    setNotice('服务端已确认赠送入账。');
    setSubmitError('');
    try {
      storage.removeItem(key);
      setPending(null);
      setGrant(EMPTY_GRANT);
    } catch {
      setPending(requestRecord);
      setGrant(requestRecord.request);
      setNotice('服务端已确认入账，但本地恢复记录未能清除。再次重试仍使用同一业务键，不会重复入账。');
    }
    showSuccess('积分赠送已记账');
  };

  const submit = async (event) => {
    event.preventDefault();
    if (inFlight.current || identityState !== 'ready' || !actor || !storage) return;
    inFlight.current = true;
    setBusy(true);
    setSubmitError('');
    setNotice('');
    try {
      let requestRecord = pending;
      let key = `${STORAGE_PREFIX}${actor.id}`;
      if (!requestRecord) {
        const request = {
          user_id: grant.user_id.trim(),
          amount_points: grant.amount_points.trim(),
          business_key: grant.business_key.trim(),
          reason: grant.reason.trim(),
        };
        if (!validRequest(request)) throw new UserMessageError('请填写有效的客户 ID、正积分金额（最多六位小数）、业务键和原因。');
        const target = await API.get(`/api/user/${Number(request.user_id)}`);
        if (target?.data?.success !== true || Number(target?.data?.data?.id) !== Number(request.user_id)) {
          throw new UserMessageError('未能确认目标客户账户，赠送操作尚未发送。请检查客户 ID 后重试。');
        }
        key = `${STORAGE_PREFIX}${actor.id}`;
        requestRecord = { version: 1, state: 'unknown', actor_id: actor.id, request };
        const serialized = JSON.stringify(requestRecord);
        storage.setItem(key, serialized);
        if (storage.getItem(key) !== serialized) throw new UserMessageError('无法可靠保存本次请求，未发送赠送操作。请检查浏览器会话存储设置后重试。');
        setPending(requestRecord);
        setGrant(request);
        setNotice('请求已安全保存，正在核验提交结果…');
      }
      await send(requestRecord, key);
    } catch (error) {
      const message = safeErrorMessage(error, '结果暂未确认。原请求仍保留，请稍后用同一请求重试。');
      setNotice('');
      setSubmitError(message);
      showError(message);
    } finally {
      inFlight.current = false;
      setBusy(false);
    }
  };

  const retryIdentity = () => installIdentity();
  const retryUsers = () => setUsersReload((current) => current + 1);
  const locked = Boolean(pending);

  return <main className='platform-page'>
    <PageTitle />
    {identityState === 'loading' && <div className='platform-state'>正在核验当前管理身份…</div>}
    {identityState === 'error' && <Alert><strong>无法确认管理身份</strong><span>{identityError}</span><button className='platform-button secondary' onClick={retryIdentity}>重试</button></Alert>}

    <section className='platform-panel'>
      <div className='platform-panel-head'><div><div className='platform-eyebrow'>CUSTOMERS</div><h2>客户账户</h2></div></div>
      {identityState === 'loading' ? <div className='platform-state'>正在核验管理身份…</div> : identityState === 'error' ? <Alert><strong>无法确认管理身份</strong><span>{identityError}</span><button className='platform-button secondary' onClick={retryIdentity}>重试</button></Alert> : usersLoading ? <div className='platform-state'>正在加载客户列表…</div> : usersError ? <Alert><strong>客户列表无法读取</strong><span>{usersError}</span><button className='platform-button secondary' onClick={retryUsers}>重试</button></Alert> : users.length === 0 ? <div className='platform-empty'><strong>{page === 0 ? '暂无客户记录' : '没有更多客户'}</strong><span>请检查其他页码，或稍后刷新。</span></div> : <div className='platform-table-wrap'><table className='platform-table'><thead><tr><th>账户 ID</th><th>用户名</th><th>显示名称</th><th>状态</th></tr></thead><tbody>{users.map((user) => <tr key={user.id}><td>{user.id}</td><td>{user.username}</td><td>{user.display_name || '—'}</td><td>{user.status === 1 ? '正常' : '受限'}</td></tr>)}</tbody></table></div>}
      {identityState === 'ready' && <div className='platform-form-footer'><span>第 {page + 1} 页 · 每页 {PAGE_SIZE} 条</span><div className='platform-actions'><button className='platform-button secondary' disabled={page === 0 || usersLoading} onClick={() => setPage((current) => Math.max(0, current - 1))}>上一页</button><button className='platform-button secondary' disabled={users.length < PAGE_SIZE || usersLoading || Boolean(usersError)} onClick={() => setPage((current) => current + 1)}>下一页</button></div></div>}
    </section>

    <section className='platform-panel'>
      <div className='platform-panel-head'><div><div className='platform-eyebrow'>AUDITED ADJUSTMENT</div><h2>赠送积分</h2></div></div>
      {storageError && <Alert><strong>赠送操作已停用</strong><span>{storageError}只读客户列表仍可使用。</span></Alert>}
      {submitError && <Alert><strong>{pending ? '原请求仍待核实' : '赠送未发送'}</strong><span>{submitError}</span></Alert>}
      {notice && <Alert kind='success'>{notice}</Alert>}
      {locked && <Alert kind='info'><strong>已锁定待确认请求</strong><span>为避免重复赠送，客户、金额、业务键和原因均已固定。刷新或重新登录后仍会恢复这笔请求；请只重试原请求。</span></Alert>}
      <form className='platform-form' onSubmit={submit}>
        <label>客户 ID<input inputMode='numeric' value={grant.user_id} onChange={(e) => update('user_id', e.target.value)} disabled={locked || !storage || identityState !== 'ready'} maxLength='16' required /></label>
        <label>赠送积分<input inputMode='decimal' value={grant.amount_points} onChange={(e) => update('amount_points', e.target.value)} disabled={locked || !storage || identityState !== 'ready'} maxLength='32' placeholder='最多六位小数' required /></label>
        <label>唯一业务键<input value={grant.business_key} onChange={(e) => update('business_key', e.target.value)} disabled={locked || !storage || identityState !== 'ready'} maxLength='149' required /></label>
        <label>调整原因<input value={grant.reason} onChange={(e) => update('reason', e.target.value)} disabled={locked || !storage || identityState !== 'ready'} maxLength='512' required /></label>
        <div className='form-wide platform-form-footer'><span>相同业务键可安全重试；不同业务键代表另一笔赠送。</span><button className='platform-button primary' disabled={busy || !storage || identityState !== 'ready'}>{busy ? '正在核验…' : locked ? '重试原请求' : '记录赠送'}</button></div>
      </form>
    </section>
  </main>;
}
