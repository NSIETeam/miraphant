import React, { useCallback, useEffect, useRef, useState } from 'react';
import { Link, useParams, useSearchParams } from 'react-router-dom';
import { StrictAPI as API } from '../../helpers';

const MICRO = 1000000;
const formatPoints = (micro = 0) => `${(Number(micro || 0) / MICRO).toLocaleString('zh-CN', { maximumFractionDigits: 6 })} 积分`;
const formatMoney = (fen = 0) => `¥${(Number(fen || 0) / 100).toFixed(2)}`;
const formatDate = (value) => value ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : '—';
const safeOrderKey = (value) => encodeURIComponent(String(value || ''));

const ORDER_STATES = [
  ['created', '订单已建立'], ['pending', '等待付款'], ['paid', '支付已确认'],
  ['credited', '已到账'], ['closed', '已关闭'], ['paid_review', '待核实'], ['refunded', '已退款'],
];
const ORDER_STATE_LABEL = Object.fromEntries(ORDER_STATES);
const AUDIT_ACTIONS = [
  ['package_publish', '发布套餐'], ['price_publish', '发布价格'], ['points_grant', '赠送积分'],
  ['offline_hold_recovery', '恢复待核实请求'], ['hold_resolution', '核实请求处理'],
  ['payment_event_quarantined', '支付通知隔离'], ['late_payment_after_close', '关闭后收到付款'],
];
const AUDIT_ACTION_LABEL = Object.fromEntries(AUDIT_ACTIONS);

function Title({ eyebrow, title, intro, action }) {
  return <div className='platform-title-row'><div><div className='platform-eyebrow'>{eyebrow}</div><h1>{title}</h1>{intro && <p>{intro}</p>}</div>{action}</div>;
}
function Busy() { return <div className='platform-state' role='status'>正在读取管理记录…</div>; }
function Empty({ title, detail }) { return <div className='platform-empty'><strong>{title}</strong><span>{detail}</span></div>; }
function Err({ error, retry }) {
  const text = error?.response?.status === 401 ? '登录状态已失效，请重新登录。'
    : error?.response?.status === 403 ? '当前账户没有查看权限，或管理权限已被撤销。'
      : error?.response?.status === 404 ? '没有找到对应记录。'
        : error?.response?.status === 429 ? '请求较频繁，请稍后再试。'
          : error?.response?.status === 400 ? '筛选条件无效，请检查编号格式和起止时间。'
          : '记录暂时无法读取，请稍后重试。';
  return <div className='platform-alert platform-alert-error' role='alert'><strong>暂时无法显示</strong><span>{text}</span>{retry && <button className='platform-button secondary' onClick={retry}>重试</button>}</div>;
}
function localDateToUTC(value) {
  if (!value) return '';
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? '' : date.toISOString();
}
function makeQuery(filters, cursor = '') {
  const query = new URLSearchParams({ limit: '30' });
  Object.entries(filters).forEach(([key, value]) => { if (value) query.set(key, value); });
  if (cursor) query.set('before_id', cursor);
  return query.toString();
}

export function AdminOrdersPage() {
  const [draft, setDraft] = useState({ order_key: '', user_id: '', channel: '', state: '', from_local: '', to_local: '' });
  const [filters, setFilters] = useState({});
  const [rows, setRows] = useState([]);
  const [cursor, setCursor] = useState('');
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState(null);
  const sequence = useRef(0);

  const loadPage = useCallback(async (before = '', append = false, applied = filters) => {
    const requestID = ++sequence.current;
    if (append) setLoadingMore(true); else setLoading(true);
    setError(null);
    try {
      const query = makeQuery(applied, before);
      const response = await API.get(`/api/admin/orders?${query}`);
      if (requestID !== sequence.current) return;
      const data = response?.data;
      if (!Array.isArray(data?.orders) || typeof data.next_cursor !== 'string') throw new Error('invalid admin orders response');
      setRows((current) => append ? [...current, ...data.orders] : data.orders);
      setCursor(data.next_cursor);
    } catch (requestError) {
      if (requestID !== sequence.current) return;
      setError(requestError);
      if (!append) setRows([]);
      setCursor('');
    } finally {
      if (requestID === sequence.current) { setLoading(false); setLoadingMore(false); }
    }
  }, [filters]);

  useEffect(() => {
    setRows([]); setCursor(''); setLoading(true);
    loadPage('', false, filters);
    return () => { sequence.current += 1; };
  }, [filters, loadPage]);

  const update = (key, value) => setDraft((current) => ({ ...current, [key]: value }));
  const submit = (event) => {
    event.preventDefault();
    const from = localDateToUTC(draft.from_local); const to = localDateToUTC(draft.to_local);
    if ((draft.from_local && !from) || (draft.to_local && !to)) { setError({ response: { status: 400 } }); return; }
    if (from && to && new Date(from) > new Date(to)) { setError({ response: { status: 400 } }); return; }
    sequence.current += 1;
    setRows([]); setCursor(''); setError(null);
    setFilters({ order_key: draft.order_key.trim(), user_id: draft.user_id.trim(), channel: draft.channel, state: draft.state, from, to });
  };
  const clear = () => {
    sequence.current += 1;
    setDraft({ order_key: '', user_id: '', channel: '', state: '', from_local: '', to_local: '' });
    setRows([]); setCursor(''); setError(null); setFilters({});
  };

  return <main className='platform-page admin-review-page'>
    <Title eyebrow='ADMIN · ORDERS' title='充值订单' intro='按订单、客户、渠道、状态和创建时间筛选。这里只查看订单快照与核验记录，不会触发查单或入账。' action={<Link className='platform-button secondary' to='/admin/audit'>查看操作审计</Link>} />
    <section className='platform-panel'>
      <form className='admin-filter-form' onSubmit={submit}>
        <label>订单编号<input value={draft.order_key} maxLength={160} onChange={(e) => update('order_key', e.target.value)} placeholder='完整订单编号' /></label>
        <label>客户编号<input type='number' min='1' step='1' value={draft.user_id} onChange={(e) => update('user_id', e.target.value)} placeholder='用户 ID' /></label>
        <label>支付渠道<select value={draft.channel} onChange={(e) => update('channel', e.target.value)}><option value=''>全部渠道</option><option value='wechat'>微信支付</option><option value='alipay'>支付宝</option></select></label>
        <label>订单状态<select value={draft.state} onChange={(e) => update('state', e.target.value)}><option value=''>全部状态</option>{ORDER_STATES.map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
        <label>开始时间<input type='datetime-local' value={draft.from_local} onChange={(e) => update('from_local', e.target.value)} /></label>
        <label>结束时间<input type='datetime-local' value={draft.to_local} onChange={(e) => update('to_local', e.target.value)} /></label>
        <div className='admin-filter-footer'><small>时间按本地输入，并转换为 UTC 后查询。</small><div><button type='button' className='platform-button secondary' onClick={clear}>清空</button><button className='platform-button primary'>应用筛选</button></div></div>
      </form>
    </section>
    <section className='platform-panel'>
      <div className='platform-panel-head'><div><div className='platform-eyebrow'>ORDER RECORDS</div><h2>订单记录</h2></div><span className='admin-result-count'>{rows.length ? `当前已加载 ${rows.length} 笔` : ''}</span></div>
      {loading ? <Busy /> : error ? <Err error={error} retry={() => loadPage()} /> : !rows.length ? <Empty title='没有符合条件的订单' detail='调整筛选条件后再试。' /> : <>
        <div className='platform-table-wrap admin-order-desktop'><table className='platform-table admin-review-table'><thead><tr><th>订单</th><th>客户</th><th>渠道</th><th>套餐</th><th>金额</th><th>积分</th><th>状态</th><th>创建时间</th><th /></tr></thead><tbody>{rows.map((row) => <tr key={row.order_key}><td><Link className='admin-record-link' to={`/admin/orders/${safeOrderKey(row.order_key)}`}>{row.order_key}</Link></td><td>#{row.user_id}</td><td>{row.channel === 'wechat' ? '微信' : row.channel === 'alipay' ? '支付宝' : row.channel}</td><td>{row.package_name || row.package_id} <small>{row.package_version}</small></td><td>{formatMoney(row.amount_fen)}</td><td>{formatPoints(Number(row.purchase_micro || 0) + Number(row.bonus_micro || 0))}</td><td><span className={`order-state-chip ${row.state === 'credited' ? 'success' : row.state === 'closed' ? 'muted' : row.state === 'paid_review' ? 'warning' : 'pending'}`}>{ORDER_STATE_LABEL[row.state] || '状态核对中'}</span></td><td>{formatDate(row.created_at)}</td><td><Link to={`/admin/orders/${safeOrderKey(row.order_key)}`}>详情 →</Link></td></tr>)}</tbody></table></div>
        <div className='admin-order-mobile'>{rows.map((row) => <Link className='admin-order-mobile-card' key={row.order_key} to={`/admin/orders/${safeOrderKey(row.order_key)}`}><div><strong>{row.order_key}</strong><span className={`order-state-chip ${row.state === 'credited' ? 'success' : row.state === 'closed' ? 'muted' : 'pending'}`}>{ORDER_STATE_LABEL[row.state] || '状态核对中'}</span></div><p>客户 #{row.user_id} · {row.channel === 'wechat' ? '微信' : row.channel === 'alipay' ? '支付宝' : row.channel}</p><p>{row.package_name || row.package_id} · {formatMoney(row.amount_fen)} · {formatPoints(Number(row.purchase_micro || 0) + Number(row.bonus_micro || 0))}</p><small>{formatDate(row.created_at)}</small></Link>)}</div>
        {cursor && <div className='admin-page-footer'><button className='platform-button secondary' disabled={loadingMore} onClick={() => loadPage(cursor, true)}>{loadingMore ? '正在加载…' : '加载更早订单'}</button></div>}
      </>}
    </section>
  </main>;
}

function EventCard({ event }) {
  return <article className='admin-event-card'><div><strong>{event.provider === 'wechat' ? '微信支付' : event.provider === 'alipay' ? '支付宝' : event.provider}</strong><span className={`order-state-chip ${event.state === 'processed' ? 'success' : event.state === 'quarantined' ? 'warning' : 'pending'}`}>{event.state === 'processed' ? '已处理' : event.state === 'quarantined' ? '已隔离' : '待处理'}</span></div><p>渠道结果：{event.status || '未提供'} · {event.currency || 'CNY'} {formatMoney(event.amount_fen)}</p><p>接收时间：{formatDate(event.created_at)}{event.provider_occurred_at ? ` · 渠道发生：${formatDate(event.provider_occurred_at)}` : ''}</p>{event.error_code && <small>核验结果：{event.error_code}</small>}{event.provider_event_hint && <small>通知编号：{event.provider_event_hint}</small>}{event.transaction_hint && <small>交易编号：{event.transaction_hint}</small>}</article>;
}

export function AdminOrderDetailPage() {
  const { orderId } = useParams();
  const [detail, setDetail] = useState(null);
  const [events, setEvents] = useState([]);
  const [eventCursor, setEventCursor] = useState('');
  const [loading, setLoading] = useState(true);
  const [loadingEvents, setLoadingEvents] = useState(false);
  const [error, setError] = useState(null);
  const [eventError, setEventError] = useState(null);
  const sequence = useRef(0);

  const load = useCallback(async (cursor = '', append = false) => {
    const requestID = ++sequence.current;
    if (append) setLoadingEvents(true); else setLoading(true);
    if (append) setEventError(null); else setError(null);
    try {
      const query = new URLSearchParams({ event_limit: '20' });
      if (cursor) query.set('event_before_id', cursor);
      const response = await API.get(`/api/admin/orders/${safeOrderKey(orderId)}?${query}`);
      if (requestID !== sequence.current) return;
      const data = response?.data;
      if (!data?.order || !Array.isArray(data.events) || typeof data.events_next_cursor !== 'string') throw new Error('invalid order detail response');
      setDetail(data);
      setEvents((current) => append ? [...current, ...data.events] : data.events);
      setEventCursor(data.events_next_cursor);
    } catch (requestError) {
      if (requestID !== sequence.current) return;
      if (append) setEventError(requestError); else { setError(requestError); setDetail(null); setEvents([]); }
    } finally {
      if (requestID === sequence.current) { setLoading(false); setLoadingEvents(false); }
    }
  }, [orderId]);
  useEffect(() => { setDetail(null); setEvents([]); setEventCursor(''); load(); return () => { sequence.current += 1; }; }, [load]);

  if (loading) return <main className='platform-page'><Busy /></main>;
  if (error) return <main className='platform-page'><Title eyebrow='ADMIN · ORDER' title='充值订单详情' action={<Link className='platform-button secondary' to='/admin/orders'>返回订单列表</Link>} /><Err error={error} retry={() => load()} /></main>;
  if (!detail?.order) return <main className='platform-page'><Empty title='订单记录不可用' detail='请返回订单列表重新打开。' /></main>;
  const order = detail.order; const ledger = detail.credit_ledger;
  return <main className='platform-page admin-review-page'>
    <Title eyebrow='ADMIN · ORDER DETAIL' title='充值订单详情' intro='订单金额与积分以创建时的确认记录为准；此页面只读，不会触发查单或改变账户余额。' action={<Link className='platform-button secondary' to='/admin/orders'>返回订单列表</Link>} />
    <section className='platform-panel'><div className='platform-panel-head'><div><div className='platform-eyebrow'>ORDER SNAPSHOT</div><h2>{order.order_key}</h2></div><span className={`order-state-chip ${order.state === 'credited' ? 'success' : order.state === 'closed' ? 'muted' : order.state === 'paid_review' ? 'warning' : 'pending'}`}>{ORDER_STATE_LABEL[order.state] || '状态核对中'}</span></div>
      <dl className='admin-order-facts'><div><dt>客户</dt><dd>#{order.user_id}</dd></div><div><dt>支付渠道</dt><dd>{order.channel === 'wechat' ? '微信支付' : order.channel === 'alipay' ? '支付宝' : order.channel}</dd></div><div><dt>套餐版本</dt><dd>{order.package_name || order.package_id} · {order.package_version}</dd></div><div><dt>订单金额</dt><dd>{formatMoney(order.amount_fen)}</dd></div><div><dt>购买积分</dt><dd>{formatPoints(order.purchase_micro)}</dd></div><div><dt>赠送积分</dt><dd>{formatPoints(order.bonus_micro)}</dd></div><div><dt>创建时间</dt><dd>{formatDate(order.created_at)}</dd></div><div><dt>订单期限</dt><dd>{formatDate(order.expires_at ? order.expires_at * 1000 : null)}</dd></div><div><dt>渠道交易编号</dt><dd>{order.provider_transaction_hint || '暂无'}</dd></div></dl>
      <div className='admin-order-detail-actions'><Link className='platform-button secondary' to={`/admin/audit?order_key=${encodeURIComponent(order.order_key)}`}>查看关联审计</Link><button className='platform-button secondary' onClick={() => load()}>刷新记录</button></div>
    </section>
    <section className='platform-panel'><div className='platform-panel-head'><div><div className='platform-eyebrow'>POINT CREDIT</div><h2>入账记录</h2></div></div>{ledger ? <div className='admin-credit-card'><span className='order-state-chip success'>积分已记账</span><strong>+{formatPoints(ledger.available_delta)}</strong><span>余额变动记录 #{ledger.id} · {formatDate(ledger.created_at)}</span></div> : <Empty title='尚未找到入账记录' detail='若订单仍在付款或核验处理中，这是正常状态。' />}</section>
    <section className='platform-panel'><div className='platform-panel-head'><div><div className='platform-eyebrow'>PAYMENT VERIFICATION</div><h2>支付核验事件</h2></div><span className='admin-result-count'>事件按接收顺序倒序显示</span></div>{eventError ? <Err error={eventError} retry={() => load(eventCursor, true)} /> : !events.length ? <Empty title='暂无核验事件' detail='收到支付平台通知或查询凭据后，记录会显示在这里。' /> : <div className='admin-event-list'>{events.map((event) => <EventCard key={event.id} event={event} />)}</div>}{eventCursor && <div className='admin-page-footer'><button className='platform-button secondary' disabled={loadingEvents} onClick={() => load(eventCursor, true)}>{loadingEvents ? '正在加载…' : '加载更早事件'}</button></div>}</section>
  </main>;
}

export function AdminAuditPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const initialOrder = searchParams.get('order_key') || '';
  const [draft, setDraft] = useState({ actor_id: '', action: '', order_key: initialOrder, request_key: '', from_local: '', to_local: '' });
  const [filters, setFilters] = useState(initialOrder ? { order_key: initialOrder } : {});
  const [rows, setRows] = useState([]);
  const [cursor, setCursor] = useState('');
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState(null);
  const sequence = useRef(0);

  const loadPage = useCallback(async (before = '', append = false, applied = filters) => {
    const requestID = ++sequence.current;
    if (append) setLoadingMore(true); else setLoading(true);
    setError(null);
    try {
      const response = await API.get(`/api/admin/audit?${makeQuery(applied, before)}`);
      if (requestID !== sequence.current) return;
      const data = response?.data;
      if (!Array.isArray(data?.audits) || typeof data.next_cursor !== 'string') throw new Error('invalid audit response');
      setRows((current) => append ? [...current, ...data.audits] : data.audits);
      setCursor(data.next_cursor);
    } catch (requestError) {
      if (requestID !== sequence.current) return;
      setError(requestError);
      if (!append) setRows([]);
      setCursor('');
    } finally {
      if (requestID === sequence.current) { setLoading(false); setLoadingMore(false); }
    }
  }, [filters]);
  useEffect(() => {
    setRows([]); setCursor(''); setLoading(true);
    loadPage('', false, filters);
    return () => { sequence.current += 1; };
  }, [filters, loadPage]);

  const update = (key, value) => setDraft((current) => ({ ...current, [key]: value }));
  const submit = (event) => {
    event.preventDefault();
    const from = localDateToUTC(draft.from_local); const to = localDateToUTC(draft.to_local);
    if ((draft.from_local && !from) || (draft.to_local && !to) || (from && to && new Date(from) > new Date(to))) { setError({ response: { status: 400 } }); return; }
    sequence.current += 1; setRows([]); setCursor(''); setError(null);
    const next = { actor_id: draft.actor_id.trim(), action: draft.action, order_key: draft.order_key.trim(), request_key: draft.request_key.trim(), from, to };
    setFilters(next);
    const params = new URLSearchParams(); if (next.order_key) params.set('order_key', next.order_key); else params.delete('order_key');
    setSearchParams(params, { replace: true });
  };
  const clear = () => {
    sequence.current += 1;
    setDraft({ actor_id: '', action: '', order_key: '', request_key: '', from_local: '', to_local: '' });
    setRows([]); setCursor(''); setError(null); setFilters({}); setSearchParams({}, { replace: true });
  };

  return <main className='platform-page admin-review-page'>
    <Title eyebrow='ADMIN · AUDIT' title='操作审计' intro='查看积分价格、套餐、赠送与请求核实记录。系统支付异常以“系统”标识，原因和关联引用只显示已知字段。' action={<Link className='platform-button secondary' to='/admin/orders'>查看充值订单</Link>} />
    <section className='platform-panel'><form className='admin-filter-form' onSubmit={submit}>
      <label>操作者编号<input type='number' min='0' step='1' value={draft.actor_id} onChange={(e) => update('actor_id', e.target.value)} placeholder='0 表示系统' /></label>
      <label>操作类型<select value={draft.action} onChange={(e) => update('action', e.target.value)}><option value=''>全部类型</option>{AUDIT_ACTIONS.map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
      <label>关联订单编号<input value={draft.order_key} maxLength={160} onChange={(e) => update('order_key', e.target.value)} placeholder='完整订单编号' /></label>
      <label>关联请求编号<input value={draft.request_key} maxLength={180} onChange={(e) => update('request_key', e.target.value)} placeholder='完整请求编号' /></label>
      <label>开始时间<input type='datetime-local' value={draft.from_local} onChange={(e) => update('from_local', e.target.value)} /></label>
      <label>结束时间<input type='datetime-local' value={draft.to_local} onChange={(e) => update('to_local', e.target.value)} /></label>
      <div className='admin-filter-footer'><small>时间按本地输入，并转换为 UTC 后查询。</small><div><button type='button' className='platform-button secondary' onClick={clear}>清空</button><button className='platform-button primary'>应用筛选</button></div></div>
    </form></section>
    <section className='platform-panel'><div className='platform-panel-head'><div><div className='platform-eyebrow'>AUDIT RECORDS</div><h2>审计记录</h2></div><span className='admin-result-count'>{rows.length ? `当前已加载 ${rows.length} 条` : ''}</span></div>
      {loading ? <Busy /> : error ? <Err error={error} retry={() => loadPage()} /> : !rows.length ? <Empty title='当前页没有匹配记录' detail={cursor ? '筛选条件仍有更早记录，请继续查找。' : '调整筛选条件后再试。'} /> : <>
        <div className='platform-table-wrap admin-audit-desktop'><table className='platform-table admin-review-table'><thead><tr><th>时间</th><th>操作者</th><th>操作</th><th>对象</th><th>原因 / 摘要</th><th>业务引用</th><th>关联</th></tr></thead><tbody>{rows.map((row) => <tr key={row.id}><td>{formatDate(row.created_at)}</td><td>{row.actor_label || (row.actor_user_id === 0 ? '系统' : `管理员 #${row.actor_user_id}`)}</td><td>{AUDIT_ACTION_LABEL[row.action] || row.action}</td><td>{row.target_user_id ? `客户 #${row.target_user_id}` : '—'}</td><td><AuditSummary row={row} /></td><td><code>{row.business_reference}</code></td><td><AuditLinks row={row} /></td></tr>)}</tbody></table></div>
        <div className='admin-audit-mobile'>{rows.map((row) => <article className='admin-audit-card' key={row.id}><div><strong>{AUDIT_ACTION_LABEL[row.action] || row.action}</strong><span>{row.actor_label || (row.actor_user_id === 0 ? '系统' : `管理员 #${row.actor_user_id}`)}</span></div><p>{formatDate(row.created_at)}{row.target_user_id ? ` · 客户 #${row.target_user_id}` : ''}</p><AuditSummary row={row} /><small>业务引用：{row.business_reference}</small><AuditLinks row={row} /></article>)}</div>
      </>}
      {!loading && !error && cursor && <div className='admin-page-footer'><button className='platform-button secondary' disabled={loadingMore} onClick={() => loadPage(cursor, true)}>{loadingMore ? '正在查找…' : rows.length ? '加载更早记录' : '继续查找更早记录'}</button></div>}
    </section>
  </main>;
}

function AuditSummary({ row }) {
  const summary = row.summary || {};
  const pieces = [];
  if (summary.package_id) pieces.push(`套餐 ${summary.package_id} · ${summary.version}`);
  if (summary.model_id) pieces.push(`模型 ${summary.model_id} · ${summary.version}`);
  if (summary.amount_micro) pieces.push(`赠送 ${formatPoints(summary.amount_micro)}`);
  if (summary.decision_action) pieces.push(`${summary.decision_action === 'settle' ? '按用量结算' : '释放冻结'}`);
  if (summary.usage_micro) pieces.push(`用量 ${formatPoints(summary.usage_micro)}`);
  if (typeof summary.activated === 'boolean') pieces.push(summary.activated ? '设为当前版本' : '仅发布版本');
  if (row.action === 'payment_event_quarantined') pieces.push('通知未自动入账，需核对');
  if (row.action === 'late_payment_after_close') pieces.push('关闭后仍收到有效付款');
  return <span className='admin-audit-summary'>{pieces.join(' · ') || '记录已保存'}{row.reason && <small>原因：{row.reason}</small>}</span>;
}
function AuditLinks({ row }) {
  return <span className='admin-audit-links'>{row.related_order_key && <Link to={`/admin/orders/${safeOrderKey(row.related_order_key)}`}>订单 {row.related_order_key}</Link>}{row.related_request_key && <code>请求 {row.related_request_key}</code>}{!row.related_order_key && !row.related_request_key && '—'}</span>;
}
