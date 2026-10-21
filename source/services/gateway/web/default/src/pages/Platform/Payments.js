import React, { useCallback, useContext, useEffect, useRef, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import QRCode from 'qrcode';
import { StrictAPI as API } from '../../helpers';
import { UserContext } from '../../context/User';
import { CustomerRefundPanel } from './Refunds';

const MICRO = 1000000;
const formatPoints = (micro = 0) => (Number(micro || 0) / MICRO).toLocaleString('zh-CN', { maximumFractionDigits: 6 });
const formatMoney = (fen = 0) => `¥${(Number(fen || 0) / 100).toFixed(2)}`;
const formatDate = (value) => value ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : '—';
const stateText = (state) => ({
  created: '订单已建立', pending: '等待付款', paid: '支付已确认，积分正在入账', paid_review: '支付已确认，正在核对',
  credited: '已到账', closed: '已关闭', refunded: '已退款', refund_pending: '退款处理中',
}[state] || '状态核对中');
const stateTone = (state) => state === 'credited' ? 'success' : state === 'closed' || state === 'refunded' ? 'muted' : state === 'paid_review' ? 'warning' : 'pending';
const safeOrderId = (value) => encodeURIComponent(String(value || ''));

function PageTitle({ eyebrow, title, intro, action }) {
  return <div className='platform-title-row'><div><div className='platform-eyebrow'>{eyebrow}</div><h1>{title}</h1>{intro && <p>{intro}</p>}</div>{action}</div>;
}
function Loading({ children = '正在加载…' }) { return <div className='platform-state' role='status'>{children}</div>; }
function ErrorBox({ children, onRetry }) { return <div className='platform-alert platform-alert-error' role='alert'><strong>暂时无法完成</strong><span>{children}</span>{onRetry && <button className='platform-button secondary' onClick={onRetry}>重试</button>}</div>; }
function Empty({ title, detail }) { return <div className='platform-empty'><strong>{title}</strong><span>{detail}</span></div>; }

function usePaymentAPIError() {
  const navigate = useNavigate();
  const [, userDispatch] = useContext(UserContext);
  return useCallback((error, fallback) => {
    if (error?.response?.status === 401) {
      localStorage.removeItem('user');
      userDispatch({ type: 'logout' });
      const path = `${window.location.pathname}${window.location.search}`;
      navigate('/login', { replace: true, state: { from: path } });
      return '登录状态已失效，请重新登录。';
    }
    if (error?.response?.status === 429) return '操作较频繁，请稍后再试。';
    if (error?.response?.status === 404) return '没有找到这笔订单，或它不属于当前账户。';
    return fallback;
  }, [navigate, userDispatch]);
}

const isCompactDevice = () => window.matchMedia('(max-width: 760px)').matches || /Android|iPhone|iPad|iPod|Mobile|MicroMessenger/i.test(navigator.userAgent || '');
function useCompactScreen() {
  const [compact, setCompact] = useState(isCompactDevice);
  useEffect(() => {
    const media = window.matchMedia('(max-width: 760px)');
    const update = () => setCompact(isCompactDevice());
    update();
    media.addEventListener?.('change', update);
    return () => media.removeEventListener?.('change', update);
  }, []);
  return compact;
}

async function paymentCSRF() {
  const response = await API.get('/api/payments/csrf');
  const token = response?.data?.csrf_token;
  if (!token) throw new Error('无法取得操作验证信息');
  return token;
}

export function WalletPage() {
  const [options, setOptions] = useState(null);
  const [wallet, setWallet] = useState(null);
  const [loading, setLoading] = useState(true);
  const [optionsError, setOptionsError] = useState('');
  const [walletUnavailable, setWalletUnavailable] = useState(false);
  const [selectedChannel, setSelectedChannel] = useState('wechat');
  const [busyPackage, setBusyPackage] = useState('');
  const [actionError, setActionError] = useState('');
  const [activeIntent, setActiveIntent] = useState(null);
  const idempotency = useRef(null);
  const createInProgress = useRef(false);
  const navigate = useNavigate();
  const explainError = usePaymentAPIError();
  const compactScreen = useCompactScreen();

  const load = useCallback(async () => {
    setLoading(true); setOptionsError('');
    const [optionsResult, walletResult] = await Promise.allSettled([
      API.get('/api/payments/options'), API.get('/api/points/wallet'),
    ]);
    if (optionsResult.status === 'fulfilled') {
      const data = optionsResult.value?.data || null;
      setOptions(data);
      const currentUser = (() => { try { return JSON.parse(localStorage.getItem('user') || 'null'); } catch { return null; } })();
      let savedIntent = null;
      try { savedIntent = JSON.parse(sessionStorage.getItem(`miraphant.payment-active-intent.${Number(currentUser?.id) || 0}`) || 'null'); } catch { /* stale local intent is ignored */ }
      if (savedIntent?.state === 'unknown') {
        setActiveIntent(savedIntent);
        setSelectedChannel(savedIntent.channel);
      } else {
        setActiveIntent(savedIntent?.state === 'known' ? savedIntent : null);
        setSelectedChannel((data?.channels || []).find((item) => item.available)?.channel || 'wechat');
      }
    } else setOptionsError(explainError(optionsResult.reason, '无法读取当前充值状态，请稍后重试。'));
    if (walletResult.status === 'fulfilled') setWallet(walletResult.value?.data || null);
    else {
      setWallet(null);
      setWalletUnavailable(true);
      explainError(walletResult.reason, '余额暂不可用。');
    }
    if (walletResult.status === 'fulfilled') setWalletUnavailable(false);
    setLoading(false);
  }, [explainError]);
  useEffect(() => { load(); }, [load]);

  const createOrder = async (pkg) => {
    if (createInProgress.current) return;
    createInProgress.current = true;
    setBusyPackage(pkg.package_id); setActionError('');
    const currentKey = `${pkg.package_id}:${selectedChannel}`;
    const currentUser = (() => { try { return JSON.parse(localStorage.getItem('user') || 'null'); } catch { return null; } })();
    const storageKey = `miraphant.payment-intent.${Number(currentUser?.id) || 0}.${currentKey}`;
    const activeStorageKey = `miraphant.payment-active-intent.${Number(currentUser?.id) || 0}`;
    let savedIntent = null;
    try { savedIntent = JSON.parse(sessionStorage.getItem(storageKey) || 'null'); } catch { sessionStorage.removeItem(storageKey); }
    if (!idempotency.current || idempotency.current.scope !== currentKey) {
      const randomKey = window.crypto?.randomUUID ? window.crypto.randomUUID() : `${Date.now()}-${Math.random().toString(36).slice(2)}`;
      idempotency.current = ['unknown', 'retryable'].includes(savedIntent?.state) && savedIntent.package_id === pkg.package_id && savedIntent.channel === selectedChannel
        ? { scope: currentKey, key: savedIntent.key }
        : { scope: currentKey, key: `ui-${randomKey}` };
    }
    let pendingIntent = null;
    try {
      const csrf = await paymentCSRF();
      pendingIntent = { state: 'unknown', key: idempotency.current.key, package_id: pkg.package_id, channel: selectedChannel };
      sessionStorage.setItem(storageKey, JSON.stringify(pendingIntent));
      sessionStorage.setItem(activeStorageKey, JSON.stringify(pendingIntent));
      setActiveIntent(pendingIntent);
      const response = await API.post('/api/payments/orders', { package_id: pkg.package_id, channel: selectedChannel }, { headers: { 'X-CSRF-Token': csrf, 'Idempotency-Key': idempotency.current.key } });
      const order = response?.data?.order;
      if (!order?.order_key) throw new Error('服务端没有返回订单信息');
      const knownIntent = { ...pendingIntent, state: 'known', order_key: order.order_key };
      sessionStorage.setItem(storageKey, JSON.stringify(knownIntent));
      sessionStorage.setItem(activeStorageKey, JSON.stringify(knownIntent));
      setActiveIntent(knownIntent);
      idempotency.current = null;
      if (response.data.checkout) navigate(`/checkout/${safeOrderId(order.order_key)}`);
      else navigate(`/console/orders/${safeOrderId(order.order_key)}`);
    } catch (error) {
      const status = error?.response?.status;
      const errorCode = error?.response?.data?.code;
      const definitelyRejected = [400, 401, 403, 422, 429].includes(status) || ['payment_schema_unavailable', 'new_orders_unavailable', 'payment_channel_unavailable', 'no_billable_model', 'package_unavailable'].includes(errorCode);
      if (definitelyRejected && pendingIntent) {
        // Keep the per-package idempotency key even after a clear rejection.
        // A concurrent same-key request may have created the order first.
        sessionStorage.setItem(storageKey, JSON.stringify({ ...pendingIntent, state: 'retryable' }));
        try {
          const active = JSON.parse(sessionStorage.getItem(activeStorageKey) || 'null');
          if (active?.key === pendingIntent.key) { sessionStorage.removeItem(activeStorageKey); setActiveIntent(null); }
        } catch { sessionStorage.removeItem(activeStorageKey); setActiveIntent(null); }
        idempotency.current = null;
      }
      setActionError(explainError(error, error?.response?.status === 409 ? '这次购买请求与已有订单不一致，请打开订单列表核对。' : '订单状态暂未确认。再次尝试会继续使用同一笔购买请求，请勿重复付款。'));
    } finally { createInProgress.current = false; setBusyPackage(''); }
  };

  const channels = options?.channels || [];
  return <main className='platform-page'>
    <PageTitle eyebrow='WALLET' title='积分与充值' intro='查看当前余额和可购买的充值套餐。跳转支付不会直接增加积分，到账以平台核验结果为准。' action={<Link className='platform-button secondary' to='/console/orders'>充值订单</Link>} />
    {loading ? <Loading /> : optionsError ? <ErrorBox onRetry={load}>{optionsError}</ErrorBox> : <>
      {walletUnavailable ? <div className='platform-alert platform-alert-warning'><strong>余额暂不可用</strong><span>充值状态仍可查看；请稍后刷新余额。</span><button className='platform-button secondary' onClick={load}>重新读取</button></div> : <>
        <section className='wallet-feature'><div><span>可用积分</span><strong>{formatPoints(wallet?.available_micro)}</strong><p>冻结 {formatPoints(wallet?.held_micro)} · 已消耗 {formatPoints(wallet?.spent_micro)}</p></div><div className='wallet-pay-status'><span className={`status-dot ${options?.purchase_available && !compactScreen ? '' : 'muted'}`} />{options?.purchase_available ? (compactScreen ? '请在电脑浏览器充值' : '当前可以创建充值订单') : '暂未开放新充值'}</div></section>
        <div className='wallet-source-grid'><div><span>购买积分 · 当前可用</span><strong>{formatPoints(wallet?.purchased_available_micro)}</strong><small>累计到账 {formatPoints(wallet?.purchased_total_micro)}</small></div><div><span>赠送积分 · 当前可用</span><strong>{formatPoints(wallet?.gifted_available_micro)}</strong><small>累计到账 {formatPoints(wallet?.gifted_total_micro)}</small></div><div><span>迁移积分 · 当前可用</span><strong>{formatPoints(wallet?.migrated_available_micro)}</strong><small>历史来源单独记录</small></div></div>
      </>}
      <section className='wallet-packages'>
        <div className='platform-panel-head'><div><div className='platform-eyebrow'>AVAILABLE PACKAGES</div><h2>充值档位</h2></div><span className='soft-tag'>¥1 = 100 积分</span></div>
        <p>订单确认后，金额、积分数量和赠送有效期会固定在这笔订单中。当前仅展示已上架套餐。</p>
        {options?.reason && <div className='platform-alert platform-alert-info package-reason'>{options.reason}</div>}
        <div className='payment-channel-picker' role='radiogroup' aria-label='选择支付方式'>
          {channels.map((channel) => <button type='button' role='radio' aria-checked={selectedChannel === channel.channel} key={channel.channel} className={`payment-channel ${selectedChannel === channel.channel ? 'selected' : ''}`} onClick={() => setSelectedChannel(channel.channel)} disabled={!channel.configured}>
            <strong>{channel.label}</strong><span>{channel.scene}</span><small>{channel.configured ? (compactScreen ? '电脑端可用' : '商户已完成配置') : '商户尚未开通'}</small>
          </button>)}
        </div>
        {options?.mobile_payment_available === false && <small className='payment-mobile-note'>当前已接入场景仅支持电脑端付款。手机浏览器和微信内支付尚未开放。</small>}
        {activeIntent?.state === 'unknown' && <div className='platform-alert platform-alert-warning'><strong>上一笔购买请求尚未确认</strong><span>再次选择同一套餐和支付方式会沿用原请求，请先核对订单状态，避免重复付款。其他套餐仍可使用。</span></div>}
        {activeIntent?.state === 'known' && activeIntent.order_key && <div className='platform-alert platform-alert-info'><span>最近创建的订单：{activeIntent.order_key}</span><Link to={`/console/orders/${safeOrderId(activeIntent.order_key)}`}>查看订单 →</Link></div>}
        {actionError && <div className='platform-alert platform-alert-error' role='alert'>{actionError}</div>}
        {!options?.packages?.length ? <Empty title='暂时没有可购买的套餐' detail='套餐上架后会显示在此处。草案档位不代表已开放销售。' /> : <div className='package-grid package-grid-live'>{options.packages.map((pkg) => {
          const channel = channels.find((item) => item.channel === selectedChannel);
          const canBuy = Boolean(options.purchase_available && channel?.available && !compactScreen);
          return <article className='live-package-card' key={`${pkg.package_id}-${pkg.version}`}><div className='package-card-top'><span className='soft-tag'>{pkg.name}</span><span className='package-version'>版本 {pkg.version}</span></div><strong>{formatMoney(pkg.amount_fen)}</strong><span className='package-points'>{formatPoints(pkg.purchase_micro)} 积分</span>{pkg.bonus_micro > 0 && <small>另赠 {formatPoints(pkg.bonus_micro)} 积分 · {Math.floor((pkg.bonus_validity_secs || 0) / 86400)} 天有效</small>}<button className='platform-button primary' disabled={!canBuy || busyPackage !== ''} onClick={() => createOrder(pkg)}>{busyPackage === pkg.package_id ? '正在创建订单…' : compactScreen && options.purchase_available ? '电脑端付款' : canBuy ? '创建充值订单' : '暂不可购买'}</button></article>;
        })}</div>}
      </section>
      <section className='platform-panel'><div className='platform-panel-head'><div><div className='platform-eyebrow'>REDEMPTION</div><h2>兑换码</h2></div></div><Empty title='兑换码充值暂不可用' detail='当前兑换入口尚未切换到积分账本，请勿使用旧版额度兑换功能。' /></section>
    </>}
  </main>;
}

export function OrdersPage() {
  const [orders, setOrders] = useState([]); const [cursor, setCursor] = useState(''); const [loaded, setLoaded] = useState(false); const [loadingMore, setLoadingMore] = useState(false); const [error, setError] = useState('');
  const explainError = usePaymentAPIError();
  const load = useCallback(async (next = '') => {
    setError(''); next ? setLoadingMore(true) : setLoaded(false);
    try {
      const response = await API.get(`/api/payments/orders?limit=30${next ? `&cursor=${encodeURIComponent(next)}` : ''}`);
      const data = response?.data || {};
      setOrders((current) => next ? [...current, ...(data.orders || [])] : (data.orders || []));
      setCursor(data.next_cursor || '');
    } catch (e) { setError(explainError(e, '订单列表暂时无法读取。')); }
    finally { setLoaded(true); setLoadingMore(false); }
  }, [explainError]);
  useEffect(() => { load(); }, [load]);
  return <main className='platform-page'><PageTitle eyebrow='ORDERS' title='充值订单' intro='仅显示当前账户创建的订单。付款后请等待平台确认，积分不会因浏览器跳转而提前增加。' action={<Link className='platform-button secondary' to='/console/wallet'>返回钱包</Link>} />
    {!loaded ? <Loading /> : error ? <ErrorBox onRetry={() => load()}>{error}</ErrorBox> : !orders.length ? <Empty title='还没有充值订单' detail='选择已上架的套餐后，订单会显示在这里。' /> : <>
      <div className='payment-order-list'>{orders.map((order) => <OrderCard key={order.order_key} order={order} />)}</div>
      {cursor && <div className='order-list-more'><button className='platform-button secondary' disabled={loadingMore} onClick={() => load(cursor)}>{loadingMore ? '正在读取…' : '读取更早订单'}</button></div>}
    </>}
  </main>;
}

function OrderCard({ order }) {
  return <Link className='payment-order-card' to={`/console/orders/${safeOrderId(order.order_key)}`}>
    <div><span className={`order-state-chip ${stateTone(order.state)}`}>{stateText(order.state)}</span><strong>{order.package_name || '充值套餐'}</strong><small>{formatDate(order.created_at)} · {order.channel === 'wechat' ? '微信支付' : '支付宝'}</small></div>
    <div className='order-card-amount'><strong>{formatMoney(order.amount_fen)}</strong><span>{formatPoints(order.purchase_micro + order.bonus_micro)} 积分</span></div>
  </Link>;
}

function OrderStatusActions({ order, onChanged, onError }) {
  const [busy, setBusy] = useState(false);
  const canQuery = ['pending', 'paid', 'paid_review'].includes(order?.state);
  const canClose = ['pending', 'created'].includes(order?.state);
  const run = async (action) => {
    setBusy(true);
    try {
      const csrf = await paymentCSRF();
      const result = await API.post(`/api/payments/orders/${safeOrderId(order.order_key)}/${action}`, {}, { headers: { 'X-CSRF-Token': csrf } });
      await onChanged(result?.data?.order);
    } catch (e) { onError(e); }
    finally { setBusy(false); }
  };
  return <div className='order-actions'>{canQuery && <button className='platform-button secondary' disabled={busy} onClick={() => run('query')}>{busy ? '正在核对…' : '核对付款状态'}</button>}{canClose && <button className='platform-button subtle-danger' disabled={busy} onClick={() => run('close')}>{busy ? '正在处理…' : '申请关闭订单'}</button>}</div>;
}

export function OrderDetailPage({ checkoutMode = false }) {
  const { orderId } = useParams();
  const [order, setOrder] = useState(null); const [checkout, setCheckout] = useState(null); const [loading, setLoading] = useState(true); const [error, setError] = useState(''); const [checkoutUnavailable, setCheckoutUnavailable] = useState(false);
  const [now, setNow] = useState(Date.now()); const [notice, setNotice] = useState('');
  const compactScreen = useCompactScreen();
  const explainError = usePaymentAPIError();
  const timer = useRef(null);
  const orderState = order?.state;
  const load = useCallback(async () => {
    setError('');
    try {
      const response = await API.get(`/api/payments/orders/${safeOrderId(orderId)}`);
      setOrder(response?.data || null);
      if (checkoutMode) {
        try {
          const saved = await API.get(`/api/payments/orders/${safeOrderId(orderId)}/checkout`);
          setCheckout(saved?.data?.checkout || null); setCheckoutUnavailable(false);
        } catch (e) {
          setCheckout(null); setCheckoutUnavailable(true);
          if (e?.response?.status === 401) throw e;
        }
      }
    } catch (e) { setError(explainError(e, e?.response?.status === 404 ? '没有找到这笔订单，或它不属于当前账户。' : '订单暂时无法读取。')); }
    finally { setLoading(false); }
  }, [checkoutMode, explainError, orderId]);
  useEffect(() => { setLoading(true); load(); }, [load]);
  useEffect(() => {
    if (!checkoutMode || !['pending', 'paid', 'paid_review'].includes(orderState)) return undefined;
    let delay = 10000; let providerDelay = 45000; let lastProviderQuery = 0; let stopped = false;
    const poll = async () => {
      if (stopped) return;
      try {
        const response = await API.get(`/api/payments/orders/${safeOrderId(orderId)}`);
        const current = response?.data;
        if (current) setOrder(current);
        if (current && ['pending', 'paid', 'paid_review'].includes(current.state) && Date.now() - lastProviderQuery >= providerDelay) {
          lastProviderQuery = Date.now();
          const csrf = await paymentCSRF();
          const queryResponse = await API.post(`/api/payments/orders/${safeOrderId(orderId)}/query`, {}, { headers: { 'X-CSRF-Token': csrf } });
          if (queryResponse?.data?.order) setOrder(queryResponse.data.order);
          providerDelay = Math.min(120000, providerDelay * 2);
        }
        delay = 10000;
      } catch (e) {
        if (e?.response?.status === 401) { explainError(e, '登录状态已失效。'); return; }
        delay = Math.min(60000, delay * 2);
        if (e?.response?.status === 429) { providerDelay = Math.min(180000, providerDelay * 2); setNotice('核对请求较频繁，系统会稍后继续。'); }
      }
      if (!stopped) timer.current = window.setTimeout(poll, delay);
    };
    timer.current = window.setTimeout(poll, delay);
    return () => { stopped = true; if (timer.current) window.clearTimeout(timer.current); };
  }, [checkoutMode, orderState, orderId, explainError]);
  useEffect(() => {
    if (!order?.expires_at) return undefined;
    const interval = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(interval);
  }, [order?.expires_at]);

  const updateOrder = async (returned) => {
    if (returned) setOrder(returned);
    else await load();
    setNotice('订单状态已更新。');
  };
  const handleActionError = (e) => setNotice(explainError(e, e?.response?.status === 409 ? '暂时无法确认这笔订单可以关闭，请先核对付款状态后再试。' : e?.response?.status === 502 || e?.response?.status === 503 ? '支付状态暂时无法确认，订单和积分会继续保留。稍后可再次核对。' : '操作未完成，请稍后重试。'));
  const remaining = order?.expires_at ? Math.max(0, Math.ceil(Number(order.expires_at) - now / 1000)) : 0;
  const timeLabel = `${String(Math.floor(remaining / 60)).padStart(2, '0')}:${String(remaining % 60).padStart(2, '0')}`;
  const canContinue = order?.state === 'pending' && order?.expires_at && Number(order.expires_at) > now / 1000;

  if (loading) return <main className='platform-page'><Loading>正在读取订单…</Loading></main>;
  if (error) return <main className='platform-page'><ErrorBox onRetry={load}>{error}</ErrorBox><Link className='platform-button secondary' to='/console/orders'>返回订单列表</Link></main>;
  if (!order) return <main className='platform-page'><Empty title='订单信息暂不可用' detail='请返回订单列表重新打开。' /></main>;
  return <main className='platform-page'>
    <PageTitle eyebrow={checkoutMode ? 'SECURE CHECKOUT' : 'ORDER DETAILS'} title={checkoutMode ? '完成充值付款' : '订单详情'} intro='付款状态以平台核验为准。浏览器返回不会直接增加积分。' action={<div className='platform-actions'>{canContinue && !checkoutMode && <Link className='platform-button primary' to={`/checkout/${safeOrderId(order.order_key)}`}>继续付款</Link>}<Link className='platform-button secondary' to='/console/orders'>全部订单</Link></div>} />
    <section className='checkout-summary'><div className='checkout-summary-title'><div><span className={`order-state-chip ${stateTone(order.state)}`}>{stateText(order.state)}</span><h2>{order.package_name || '充值套餐'}</h2><small>订单号 {order.order_key}</small></div><div className='checkout-price'><span>应付金额</span><strong>{formatMoney(order.amount_fen)}</strong></div></div>
      <div className='checkout-facts'><div><span>基础积分</span><strong>{formatPoints(order.purchase_micro)}</strong></div><div><span>赠送积分</span><strong>{formatPoints(order.bonus_micro)}</strong></div><div><span>预计到账</span><strong>{formatPoints(order.purchase_micro + order.bonus_micro)}</strong></div><div><span>支付方式</span><strong>{order.channel === 'wechat' ? '微信支付' : '支付宝'}</strong></div><div><span>创建时间</span><strong>{formatDate(order.created_at)}</strong></div><div><span>付款期限</span><strong>{order.expires_at ? formatDate(Number(order.expires_at) * 1000) : '—'}</strong></div></div>
    </section>
    {checkoutMode && order.state === 'pending' && remaining > 0 && checkout && (compactScreen ? <div className='platform-alert platform-alert-warning'><strong>当前支付方式仅支持电脑端</strong><span>请在电脑上打开此订单完成付款。手机端仍可查看并核对订单状态。</span></div> : <CheckoutMaterial checkout={checkout} channel={order.channel} />)}
    {checkoutMode && order.state === 'pending' && remaining > 0 && !checkout && <div className='platform-alert platform-alert-warning'><strong>支付信息暂不可用</strong><span>订单仍保留，可核对付款状态或稍后重试读取；请勿重复创建订单。</span></div>}
    {checkoutMode && order.state === 'pending' && remaining > 0 && <div className='checkout-countdown'>请在 <strong>{timeLabel}</strong> 内完成付款；页面会定期核对状态。</div>}
    {checkoutMode && order.state === 'pending' && remaining === 0 && <div className='platform-alert platform-alert-warning'><strong>付款时间已结束</strong><span>请先核对这笔订单。只有确认关闭后，才能创建新的充值订单。</span></div>}
    {notice && <div className='platform-alert platform-alert-info' role='status'>{notice}</div>}
    <OrderStatusActions order={order} onChanged={updateOrder} onError={handleActionError} />
    {checkoutUnavailable && order.state !== 'pending' && <div className='platform-alert platform-alert-info'>此订单目前没有可用的付款页面，可查看最新状态或返回订单列表。</div>}
    {order.state === 'credited' && <div className='platform-alert platform-alert-success'><strong>积分已到账</strong><span>本笔订单增加 {formatPoints(order.purchase_micro + order.bonus_micro)} 积分。</span><Link to='/console/wallet'>查看钱包 →</Link></div>}
    {['credited', 'refunded'].includes(order.state) && <CustomerRefundPanel order={order} />}
    <div className='checkout-help-note'>若付款后状态暂未更新，请点击“核对付款状态”。请勿重复付款；如长时间未到账，请联系平台客服并提供订单号。</div>
  </main>;
}

function CheckoutMaterial({ checkout, channel }) {
  const canvas = useRef(null); const [qrError, setQrError] = useState('');
  useEffect(() => {
    if (checkout?.kind !== 'qr' || !checkout.code_url || !canvas.current) return undefined;
    let active = true;
    QRCode.toCanvas(canvas.current, checkout.code_url, { width: 240, margin: 2, errorCorrectionLevel: 'M', color: { dark: '#132f27', light: '#ffffff' } })
      .then(() => { if (active) setQrError(''); }).catch(() => { if (active) setQrError('付款码暂时无法显示，请刷新订单页面。'); });
    return () => { active = false; };
  }, [checkout]);
  if (checkout?.kind === 'qr' && channel === 'wechat' && checkout.code_url) {
    return <section className='checkout-material'><div className='platform-eyebrow'>WECHAT NATIVE</div><h2>使用微信扫码付款</h2><div className='checkout-qr-frame'><canvas ref={canvas} aria-label='微信支付二维码' />{qrError && <span>{qrError}</span>}</div><p>打开微信扫一扫完成付款。请勿截图后转发；付款状态以本页核对结果为准。</p></section>;
  }
  const gateway = (() => { try { return new URL(checkout?.gateway_url || ''); } catch { return null; } })();
  const safeGateway = gateway?.protocol === 'https:' && gateway.hostname === 'openapi.alipay.com' && gateway.pathname === '/gateway.do' && !gateway.search && !gateway.hash;
  const fields = Object.entries(checkout?.fields || {}).filter(([key, value]) => /^[A-Za-z_]+$/.test(key) && typeof value === 'string' && value.length <= 32768);
  if (checkout?.kind === 'form' && channel === 'alipay' && safeGateway && fields.length > 0) {
    return <section className='checkout-material checkout-alipay'><div className='platform-eyebrow'>ALIPAY PAGE PAY</div><h2>前往支付宝完成付款</h2><p>提交后会打开支付宝官方收银台。支付结果返回本页后仍需平台核验，页面跳转不会提前增加积分。</p><form action='https://openapi.alipay.com/gateway.do' method='POST'>{fields.map(([key, value]) => <input key={key} type='hidden' name={key} value={value} />)}<button className='platform-button primary'>前往支付宝付款</button></form></section>;
  }
  return <div className='platform-alert platform-alert-error'><strong>付款信息格式无法识别</strong><span>为保障订单安全，本页没有打开未知支付地址。请联系平台客服并提供订单号。</span></div>;
}

export function AdminPackagesPage() {
  const [rows, setRows] = useState([]); const [loading, setLoading] = useState(true); const [error, setError] = useState(''); const [busy, setBusy] = useState(false); const [saved, setSaved] = useState('');
  const [form, setForm] = useState(() => {
    try { return JSON.parse(sessionStorage.getItem('miraphant.admin-package-draft') || 'null') || { package_id: '', version: '', name: '', amount_yuan: '', bonus_points: '0', bonus_days: '0', activate: true }; }
    catch { sessionStorage.removeItem('miraphant.admin-package-draft'); return { package_id: '', version: '', name: '', amount_yuan: '', bonus_points: '0', bonus_days: '0', activate: true }; }
  });
  const explainError = usePaymentAPIError();
  const load = useCallback(async () => {
    setError('');
    try { const response = await API.get('/api/admin/points/payments/packages'); setRows(response?.data?.packages || []); }
    catch (e) { setError(explainError(e, '套餐版本和审计记录暂时无法读取。')); }
    finally { setLoading(false); }
  }, [explainError]);
  useEffect(() => { load(); }, [load]);
  const update = (key, value) => setForm((current) => {
    const next = { ...current, [key]: value };
    sessionStorage.setItem('miraphant.admin-package-draft', JSON.stringify(next));
    return next;
  });
  const publish = async (event) => {
    event.preventDefault(); setBusy(true); setSaved(''); setError('');
    const amountFen = Math.round(Number(form.amount_yuan) * 100);
    const bonusMicro = Math.round(Number(form.bonus_points || 0) * MICRO);
    const bonusValiditySecs = Number(form.bonus_days || 0) * 86400;
    if (!form.package_id.trim() || !form.version.trim() || !form.name.trim() || !Number.isSafeInteger(amountFen) || amountFen <= 0 || !Number.isSafeInteger(bonusMicro) || bonusMicro < 0 || !Number.isSafeInteger(bonusValiditySecs) || (bonusMicro > 0 && bonusValiditySecs <= 0) || (bonusMicro === 0 && bonusValiditySecs !== 0)) { setError('请填写有效的套餐编号、版本、名称、金额和赠送期限。'); setBusy(false); return; }
    const payload = { package_id: form.package_id.trim(), version: form.version.trim(), name: form.name.trim(), amount_fen: amountFen, bonus_micro: bonusMicro, bonus_validity_secs: bonusValiditySecs, activate: Boolean(form.activate) };
    const scope = `miraphant.admin-package-intent.${payload.package_id}.${payload.version}`;
    const fingerprint = JSON.stringify(payload);
    let intent = null;
    try { intent = JSON.parse(sessionStorage.getItem(scope) || 'null'); } catch { sessionStorage.removeItem(scope); }
    if (intent?.state === 'unknown' && intent.fingerprint !== fingerprint) {
      setError('这组套餐信息已有一笔结果待确认。请恢复原表单后重试，或使用新的版本号。'); setBusy(false); return;
    }
    const existing = rows.some((row) => row.package_id === payload.package_id && row.version === payload.version);
    if (existing && intent?.state !== 'unknown') {
      setError('该套餐版本已经发布。请使用新的版本号，避免改变历史销售版本。'); setBusy(false); return;
    }
    const businessKey = intent?.state === 'unknown' ? intent.business_key : `package-ui-${window.crypto?.randomUUID ? window.crypto.randomUUID() : `${Date.now()}-${Math.random().toString(36).slice(2)}`}`;
    sessionStorage.setItem(scope, JSON.stringify({ state: 'unknown', business_key: businessKey, fingerprint }));
    try {
      const csrf = await paymentCSRF();
      await API.post('/api/admin/points/payments/packages', { ...payload, business_key: businessKey }, { headers: { 'X-CSRF-Token': csrf } });
      sessionStorage.removeItem(scope);
      sessionStorage.removeItem('miraphant.admin-package-draft');
      setSaved('新套餐版本已发布。历史订单仍保留创建时的套餐内容。');
      await load();
      setForm((current) => ({ ...current, version: '', name: '', amount_yuan: '', bonus_points: '0', bonus_days: '0' }));
    } catch (e) { setError(explainError(e, e?.response?.status === 409 ? '版本发布与已记录操作冲突，请检查套餐版本和表单内容。' : '发布结果暂未确认。再次提交会复用同一发布请求。')); }
    finally { setBusy(false); }
  };
  return <main className='platform-page'><PageTitle eyebrow='ADMIN · PACKAGES' title='充值套餐' intro='每次发布会创建不可变版本。已创建订单继续使用当时冻结的金额、积分和赠送期限。' action={<Link className='platform-button secondary' to='/admin'>管理首页</Link>} />
    {error && <div className='platform-alert platform-alert-error' role='alert'>{error}</div>}{saved && <div className='platform-alert platform-alert-success' role='status'>{saved}</div>}
    <section className='platform-panel'><div className='platform-panel-head'><div><div className='platform-eyebrow'>PUBLISH VERSION</div><h2>发布套餐新版本</h2></div><span className='soft-tag'>1 元 = 100 积分</span></div><form className='platform-form' onSubmit={publish}><label>套餐编号<input value={form.package_id} onChange={(e) => update('package_id', e.target.value)} maxLength='80' required placeholder='例如 starter' /></label><label>版本号<input value={form.version} onChange={(e) => update('version', e.target.value)} maxLength='80' required placeholder='例如 v2' /></label><label>显示名称<input value={form.name} onChange={(e) => update('name', e.target.value)} maxLength='160' required /></label><label>金额（元）<input type='number' min='0.01' step='0.01' value={form.amount_yuan} onChange={(e) => update('amount_yuan', e.target.value)} required /></label><label>赠送积分<input type='number' min='0' step='0.000001' value={form.bonus_points} onChange={(e) => update('bonus_points', e.target.value)} /></label><label>赠送有效期（天）<input type='number' min='0' step='1' value={form.bonus_days} onChange={(e) => update('bonus_days', e.target.value)} /></label><label className='package-activate-toggle'><input type='checkbox' checked={form.activate} onChange={(e) => update('activate', e.target.checked)} />发布后设为当前销售版本</label><div className='form-wide platform-form-footer'><span>存在赠送积分时，必须设置大于 0 的有效期。套餐编号与版本号发布后不可修改。</span><button className='platform-button primary' disabled={busy}>{busy ? '正在发布…' : '发布新版本'}</button></div></form></section>
    <section className='platform-panel package-history-panel'><div className='platform-panel-head'><div><div className='platform-eyebrow'>VERSION HISTORY</div><h2>已发布版本</h2></div><button className='platform-button secondary' disabled={loading} onClick={load}>刷新</button></div>{loading ? <Loading /> : error ? <Empty title='版本记录暂不可用' detail='请检查管理权限后重试。' /> : !rows.length ? <Empty title='还没有已发布套餐' detail='发布后的套餐版本会在这里保留。' /> : <div className='platform-table-wrap'><table className='platform-table'><thead><tr><th>套餐</th><th>版本</th><th>金额</th><th>积分</th><th>赠送</th><th>状态</th><th>发布审计</th></tr></thead><tbody>{rows.map((row) => <tr key={`${row.package_id}-${row.version}`}><td><strong>{row.name}</strong><small>{row.package_id}</small></td><td>{row.version}</td><td>{formatMoney(row.amount_fen)}</td><td>{formatPoints(row.purchase_micro)}</td><td>{row.bonus_micro ? `${formatPoints(row.bonus_micro)} · ${Math.floor(row.bonus_validity_secs / 86400)}天` : '无'}</td><td><span className={`order-state-chip ${row.active ? 'success' : 'muted'}`}>{row.active ? '当前销售' : '历史版本'}</span></td><td>{row.audit ? <small>管理员 {row.audit.actor_user_id}<br />{formatDate(row.audit.created_at)}<br /><code>{row.audit.business_key}</code></small> : <small>审计记录未关联</small>}</td></tr>)}</tbody></table></div>}</section>
  </main>;
}

export function AdminPaymentsPage() {
  const [status, setStatus] = useState(null); const [loading, setLoading] = useState(true); const [error, setError] = useState('');
  const explainError = usePaymentAPIError();
  const load = useCallback(async () => { setError(''); try { const response = await API.get('/api/admin/points/payments/status'); setStatus(response?.data || null); } catch (e) { setError(explainError(e, '支付配置状态暂时无法读取。')); } finally { setLoading(false); } }, [explainError]);
  useEffect(() => { load(); }, [load]);
  return <main className='platform-page'><PageTitle eyebrow='ADMIN · PAYMENTS' title='支付准备状态' intro='此页面只显示网关的脱敏就绪状态。商户凭据由服务器环境和受限文件管理。' action={<Link className='platform-button secondary' to='/admin'>管理首页</Link>} />
    {loading ? <Loading /> : error ? <ErrorBox onRetry={load}>{error}</ErrorBox> : <>
      <div className='payment-switch-grid'><div><span>新充值开关</span><strong className={status?.new_orders_enabled ? 'status-enabled' : 'status-disabled'}>{status?.new_orders_enabled ? '已开启' : '已关闭'}</strong><small>关闭新充值不会停止历史订单核验</small></div><div><span>积分计费</span><strong className={status?.points_billing_enabled ? 'status-enabled' : 'status-disabled'}>{status?.points_billing_enabled ? '已开启' : '已关闭'}</strong><small>开启前需完成账本迁移与验收</small></div><div><span>手机支付场景</span><strong className='status-disabled'>未开放</strong><small>当前仅显示已接入的电脑端场景</small></div></div>
      <section className='platform-panel'><div className='platform-panel-head'><div><div className='platform-eyebrow'>CHANNEL READINESS</div><h2>服务端渠道状态</h2></div><button className='platform-button secondary' onClick={load}>刷新状态</button></div><p>{status?.credentials_source}</p><div className='payment-provider-list'>{(status?.providers || []).map((provider) => <article key={provider.channel}><div><strong>{provider.label}</strong><span>{provider.scene}</span></div><span className={`order-state-chip ${provider.configured ? 'success' : 'warning'}`}>{provider.configured ? '服务端配置已就绪' : '尚未配置／开通'}</span></article>)}</div></section>
      <section className='platform-panel'><div className='platform-eyebrow'>OPENING CHECKLIST</div><h2>开通前准备</h2><ol className='payment-requirements'>{(status?.requirements || []).map((item) => <li key={item}>{item}</li>)}</ol><div className='platform-alert platform-alert-info'><strong>当前支付范围</strong><span>微信 Native 扫码与支付宝电脑网页支付。微信 H5／JSAPI、支付宝手机网页支付、退款和全量对账尚未实现；不要把手机页面布局视为已开通手机支付。</span></div></section>
    </>}
  </main>;
}
