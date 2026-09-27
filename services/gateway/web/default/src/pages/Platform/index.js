import React, { useContext, useEffect, useState } from 'react';
import { Link, Navigate, useNavigate } from 'react-router-dom';
import { UserContext } from '../../context/User';
import { StrictAPI as API, copy, showError, showSuccess } from '../../helpers';
export { WalletPage, OrdersPage, OrderDetailPage, AdminPackagesPage, AdminPaymentsPage } from './Payments';
export { AdminOrdersPage, AdminOrderDetailPage, AdminAuditPage } from './AdminPaymentReview';
export { CustomerRefundPanel, AdminRefundsPage, AdminRefundDetailPage, RefundCapabilityManagementPage, RefundCapabilityRoute } from './Refunds';
export { AdminReconciliationPage } from './Reconciliation';
export { BrandStatusPage } from './Brand';

const MICRO = 1000000;
const formatPoints = (micro = 0) => (Number(micro || 0) / MICRO).toLocaleString('zh-CN', { maximumFractionDigits: 6 });
const formatRate = (micro = 0) => formatPoints(micro);
const formatDate = (value) => value ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : '—';
const userFromStorage = () => {
  try { return JSON.parse(localStorage.getItem('user') || 'null'); } catch { return null; }
};

function PageTitle({ eyebrow, title, intro, action }) {
  return <div className='platform-title-row'><div><div className='platform-eyebrow'>{eyebrow}</div><h1>{title}</h1>{intro && <p>{intro}</p>}</div>{action}</div>;
}

function LoadingState() { return <div className='platform-state'>正在加载…</div>; }
function EmptyState({ title, detail }) { return <div className='platform-empty'><strong>{title}</strong><span>{detail}</span></div>; }
function ErrorState({ title = '暂时无法连接服务', detail = '请稍后刷新，或联系平台支持。' }) { return <div className='platform-alert platform-alert-error'><strong>{title}</strong><span>{detail}</span></div>; }

export function PlatformHome() {
  const [prices, setPrices] = useState([]);
  const [loaded, setLoaded] = useState(false);
  const [failed, setFailed] = useState(false);
  const [userState] = useContext(UserContext);
  const user = userState.user || userFromStorage();
  useEffect(() => {
    API.get('/api/points/prices').then((res) => setPrices(res?.data?.prices || [])).catch(() => setFailed(true)).finally(() => setLoaded(true));
  }, []);
  return <main className='platform-landing'>
    <section className='platform-hero'>
      <div className='platform-hero-copy'>
        <div className='platform-eyebrow'>MIRAPHANT MODEL PLATFORM</div>
        <h1>让模型服务<br /><em>清晰、可控、可计量</em></h1>
        <p>统一接入模型 API，以积分查看每次调用的真实用量。价格公开，账单可查，密钥预算由你掌握。</p>
        <div className='platform-actions'><Link className='platform-button primary' to={user ? '/console' : '/register'}>{user ? '进入控制台' : '创建账户'}</Link><Link className='platform-button secondary' to='/pricing'>查看价格</Link></div>
        <div className='platform-hero-footnote'>当前模型与充值状态以服务端实际配置为准。</div>
      </div>
      <div className='platform-hero-card'><img src='/miraphant.svg' alt='Miraphant' /><div className='platform-orbit orbit-one' /><div className='platform-orbit orbit-two' /><div className='platform-hero-card-note'><span className='status-dot' /> 积分与账单透明可查</div></div>
    </section>
    <section className='platform-section'><div className='platform-section-heading'><div><div className='platform-eyebrow'>HOW IT WORKS</div><h2>一套简单的使用方式</h2></div><Link to='/help' className='platform-text-link'>接入说明 →</Link></div>
      <div className='platform-feature-grid'><article><span className='feature-index'>01</span><h3>查看生效价格</h3><p>按模型展示当前版本价格。估算会标明假设，最终账单依据上游返回的可信用量。</p></article><article><span className='feature-index'>02</span><h3>创建访问密钥</h3><p>为每个密钥设置积分预算和有效期，限制密钥可累计使用的积分。</p></article><article><span className='feature-index'>03</span><h3>核对每次用量</h3><p>钱包区分可用、冻结与已消耗积分。用量待核实期间，冻结金额会保留。</p></article></div>
    </section>
    <section className='platform-model-strip'><div><div className='platform-eyebrow'>MODEL AVAILABILITY</div><h2>已发布的模型</h2></div>{!loaded ? <span>正在检查…</span> : failed ? <div className='platform-unavailable'><strong>暂时无法读取模型状态</strong><span>请稍后刷新页面。</span></div> : prices.length ? <div className='platform-model-pills'>{prices.slice(0, 5).map((item) => <span key={item.model}>{item.model}</span>)}</div> : <div className='platform-unavailable'><strong>暂未开放模型调用</strong><span>平台尚未发布可用模型价格，请稍后查看。</span></div>}</section>
  </main>;
}

export function PricingPage() {
  const [prices, setPrices] = useState([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(false);
  const [model, setModel] = useState('');
  const [prompt, setPrompt] = useState('');
  const [maxOutput, setMaxOutput] = useState(512);
  const [estimate, setEstimate] = useState(null);
  const [estimating, setEstimating] = useState(false);
  useEffect(() => {
    API.get('/api/points/prices').then((res) => {
      const active = res?.data?.prices || [];
      setPrices(active);
      setModel(active[0]?.model || '');
    }).catch(() => setError(true)).finally(() => setLoading(false));
  }, []);
  const runEstimate = async (event) => {
    event.preventDefault();
    setEstimating(true); setEstimate(null);
    try {
      const res = await API.post('/api/points/estimate', { model, prompt, max_output_tokens: Number(maxOutput) });
      if (res?.data) setEstimate(res.data);
    } catch (_) { setEstimate({ error: '估算暂不可用，请登录后重试。' }); }
    finally { setEstimating(false); }
  };
  return <main className='platform-page'>
    <PageTitle eyebrow='PRICING' title='模型与积分价格' intro='只展示服务端已发布的生效价格，按每千个 token 标价；估算值不等于最终账单。' />
    <section className='platform-panel draft-scenarios'><div className='platform-panel-head'><div><div className='platform-eyebrow'>PLANNING EXAMPLES</div><h2>任务消耗示例（价格草案）</h2></div><span className='soft-tag'>仅供规划，不是生效价格</span></div><p>按标准档草案费率估算，假设一次调用、无缓存、无额外工具费用；模型成本复核后才会发布正式价格。</p><div className='scenario-grid'><div><strong>约 0.85 积分</strong><span>简短问答 · 500 输入 / 300 输出 token</span><small>1,000 积分约可完成 1,176 次</small></div><div><strong>约 2.10 积分</strong><span>商品文案初稿 · 1,000 输入 / 800 输出 token</span><small>1,000 积分约可完成 476 次</small></div><div><strong>约 6 积分</strong><span>长文摘要 · 8,000 输入 / 1,000 输出 token</span><small>1,000 积分约可完成 166 次</small></div><div><strong>约 7.50 积分</strong><span>较长翻译 · 3,000 输入 / 3,000 输出 token</span><small>1,000 积分约可完成 133 次</small></div><div><strong>约 11 积分</strong><span>带上下文的代码问答 · 10,000 输入 / 3,000 输出 token</span><small>1,000 积分约可完成 90 次</small></div></div></section>
    {loading ? <LoadingState /> : error ? <ErrorState /> : prices.length === 0 ? <EmptyState title='当前没有已发布价格' detail='上方示例为未生效草案。模型正式开放前，平台不会按草案计费。' /> : <>
      <div className='platform-table-wrap'><table className='platform-table'><thead><tr><th>模型</th><th>版本</th><th>输入 / 1K</th><th>缓存输入 / 1K</th><th>输出 / 1K</th><th>附加费用</th></tr></thead><tbody>{prices.map((item) => <tr key={`${item.model}-${item.version}`}><td><strong>{item.model}</strong></td><td><span className='platform-version'>{item.version}</span></td><td>{formatRate(item.input_micro_per_1k)} 积分</td><td>{formatRate(item.cached_input_micro_per_1k)} 积分</td><td>{formatRate(item.output_micro_per_1k)} 积分</td><td>{formatRate(item.extra_micro)} 积分</td></tr>)}</tbody></table></div>
      <section className='platform-panel estimate-panel'><div className='platform-panel-head'><div><div className='platform-eyebrow'>ESTIMATE</div><h2>试算一次请求</h2></div><span className='soft-tag'>估算值，不是最终账单</span></div>
        <form className='platform-form' onSubmit={runEstimate}><label>模型<select value={model} onChange={(e) => setModel(e.target.value)}>{prices.map((item) => <option key={item.model} value={item.model}>{item.model}</option>)}</select></label><label>最大输出 token<input type='number' min='1' max='8192' value={maxOutput} onChange={(e) => setMaxOutput(e.target.value)} /></label><label className='form-wide'>提示内容<textarea value={prompt} onChange={(e) => setPrompt(e.target.value)} maxLength={131072} rows='5' placeholder='输入一段用于估算的示例文本' required /></label><div className='form-wide platform-form-footer'><span>估算以文本字节数近似输入 token，输出按最大值预留。</span><button className='platform-button primary' disabled={estimating || !prompt.trim()}>{estimating ? '正在估算…' : '估算积分'}</button></div></form>
        {estimate && <div className={estimate.error ? 'platform-alert platform-alert-error' : 'platform-estimate-result'}>{estimate.error ? estimate.error : <><div><span>预留上限</span><strong>{formatPoints(estimate.budget_micro)} 积分</strong></div><p>价格版本 {estimate.price_version} · 输入约 {estimate.estimated_prompt_tokens} token · 输出最多 {estimate.max_output_tokens} token</p><small>按提示文本字节数近似输入 token，缓存内容按普通输入价估算，输出按最大值预留。最终费用以可信上游用量为准。</small></>}</div>}
      </section>
    </>}
  </main>;
}

export function ConsolePage() {
  const [wallet, setWallet] = useState(null); const [usage, setUsage] = useState([]); const [loading, setLoading] = useState(true); const [failed, setFailed] = useState(false);
  const load = async () => { setLoading(true); setFailed(false); try { const [walletRes, usageRes] = await Promise.all([API.get('/api/points/wallet'), API.get('/api/points/usage?limit=5')]); setWallet(walletRes?.data); setUsage(usageRes?.data?.usage || []); } catch (_) { setFailed(true); } finally { setLoading(false); } };
  useEffect(() => { load(); }, []);
  return <main className='platform-page'><PageTitle eyebrow='YOUR CONSOLE' title='账户总览' intro='查看积分余额、待核实用量和最近请求。' action={<Link to='/console/wallet' className='platform-button primary'>积分与充值</Link>} />
    {loading ? <LoadingState /> : failed ? <ErrorState /> : <>
      <div className='wallet-metrics'><div className='wallet-metric metric-main'><span>可用积分</span><strong>{formatPoints(wallet?.available_micro)}</strong><small>可用于新的模型请求</small></div><div className='wallet-metric'><span>冻结中</span><strong>{formatPoints(wallet?.held_micro)}</strong><small>待核实请求会保留冻结</small></div><div className='wallet-metric'><span>已消耗</span><strong>{formatPoints(wallet?.spent_micro)}</strong><small>按可信上游用量结算</small></div></div>
      <div className='platform-panel'><div className='platform-panel-head'><div><div className='platform-eyebrow'>RECENT ACTIVITY</div><h2>最近用量</h2></div><Link to='/console/usage' className='platform-text-link'>全部账单 →</Link></div><UsageTable rows={usage.slice(0, 5)} /></div>
      <div className='platform-shortcuts'><Link to='/console/keys'><span>01</span><b>管理访问密钥</b><i>预算、有效期与撤销</i><strong>→</strong></Link><Link to='/pricing'><span>02</span><b>查看价格与估算</b><i>以生效版本为准</i><strong>→</strong></Link><Link to='/help'><span>03</span><b>API 接入帮助</b><i>快速开始与计量说明</i><strong>→</strong></Link></div>
    </>}
  </main>;
}

function UsageTable({ rows }) {
  if (!rows?.length) return <EmptyState title='还没有用量记录' detail='请求完成后，账单会显示在这里。' />;
  return <div className='platform-table-wrap'><table className='platform-table'><thead><tr><th>时间</th><th>模型</th><th>状态</th><th>积分</th><th>版本</th></tr></thead><tbody>{rows.map((row) => <tr key={row.request_id}><td>{formatDate(row.created_at)}</td><td>{row.model_id || '—'}</td><td><span className={`state-chip state-${row.state}`}>{stateLabel(row.state)}</span></td><td>{formatPoints(row.usage_micro)}{row.state === 'pending' || row.state === 'needs_review' ? <small> · 冻结 {formatPoints(row.budget_micro)}</small> : ''}</td><td>{row.price_version || '—'}</td></tr>)}</tbody></table></div>;
}
const stateLabel = (state) => ({ settled: '已结算', pending: '待核实', needs_review: '超出预留', released: '已释放' }[state] || state || '未知');

export function UsagePage() {
  const [rows, setRows] = useState([]); const [loading, setLoading] = useState(true); const [error, setError] = useState(false);
  const load = () => API.get('/api/points/usage?limit=100').then((res) => setRows(res?.data?.usage || [])).catch(() => setError(true)).finally(() => setLoading(false));
  useEffect(() => { load(); }, []);
  return <main className='platform-page'><PageTitle eyebrow='USAGE' title='用量账单' intro='账单展示模型、价格版本、预留积分及真实上游用量。待核实记录不会按本地估算扣款。' />{loading ? <LoadingState /> : error ? <ErrorState title='账单暂时不可用' /> : <div className='platform-panel'><UsageTable rows={rows} /></div>}</main>;
}

export function KeysPage() {
  const [tokens, setTokens] = useState([]); const [loading, setLoading] = useState(true); const [failed, setFailed] = useState(false); const [name, setName] = useState(''); const [budget, setBudget] = useState('20'); const [expiryDays, setExpiryDays] = useState('-1'); const [models, setModels] = useState([]); const [allowed, setAllowed] = useState([]); const [createdKey, setCreatedKey] = useState(''); const [busy, setBusy] = useState(false);
  const load = async () => { setLoading(true); setFailed(false); try { const [tokenRes, modelRes] = await Promise.all([API.get('/api/points/tokens'), API.get('/api/user/available_models')]); setTokens(tokenRes?.data?.tokens || []); setAllowed(modelRes?.data?.data || []); } catch (_) { setFailed(true); } finally { setLoading(false); } };
  useEffect(() => { load(); }, []);
  const create = async (event) => { event.preventDefault(); setBusy(true); setCreatedKey(''); try { const csrf = await API.get('/api/points/csrf'); const csrfToken = csrf?.data?.csrf_token; if (!csrfToken) throw new Error('无法取得安全令牌'); const expiry = expiryDays === '-1' ? -1 : Math.floor(Date.now() / 1000) + Number(expiryDays) * 86400; const res = await API.post('/api/token/', { name, expired_time: expiry, models: models.join(','), subnet: '' }, { headers: { 'X-CSRF-Token': csrfToken } }); const token = res?.data?.data; if (!res?.data?.success || !token?.id) throw new Error(res?.data?.message || '密钥创建失败'); setCreatedKey(token.key || ''); const budgetRes = await API.put(`/api/points/tokens/${token.id}/budget`, { limit_points: budget }, { headers: { 'X-CSRF-Token': csrfToken } }); if (!budgetRes?.data?.success) throw new Error('密钥已创建，但积分预算设置未确认。请在列表中重试设置预算。'); showSuccess('访问密钥已创建，密钥仅在本次显示。'); setName(''); await load(); } catch (error) { showError(error.message || '访问密钥创建失败'); } finally { setBusy(false); } };
  const setTokenBudget = async (id, value) => { const csrf = await API.get('/api/points/csrf'); const token = csrf?.data?.csrf_token; if (!token) { showError('无法取得安全令牌'); return; } try { const res = await API.put(`/api/points/tokens/${id}/budget`, { limit_points: value }, { headers: { 'X-CSRF-Token': token } }); if (res?.data?.success) { showSuccess('积分预算已更新'); await load(); } else showError(res?.data?.error || '预算更新失败'); } catch (_) { showError('预算更新失败'); } };
  const setTokenSettings = async (id, expiredAt, status) => { try { const csrf = await API.get('/api/points/csrf'); const res = await API.put(`/api/points/tokens/${id}/settings`, { expired_time: expiredAt, status }, { headers: { 'X-CSRF-Token': csrf?.data?.csrf_token } }); if (res?.data?.success) { showSuccess('密钥设置已更新'); await load(); } else showError(res?.data?.error || '设置更新失败'); } catch (_) { showError('设置更新失败'); } };
  const revoke = async (id) => { if (!window.confirm('撤销后该访问密钥将无法继续使用。确定撤销？')) return; try { const csrf = await API.get('/api/points/csrf'); const res = await API.delete(`/api/token/${id}/`, { headers: { 'X-CSRF-Token': csrf?.data?.csrf_token } }); if (res?.data?.success) { showSuccess('访问密钥已撤销'); await load(); } else showError(res?.data?.message || '撤销失败'); } catch (_) { showError('撤销失败'); } };
  return <main className='platform-page'><PageTitle eyebrow='ACCESS KEYS' title='访问密钥' intro='密钥用于 API 身份验证。为每个密钥设置积分预算；完整密钥仅在创建时展示。' />
    <section className='platform-panel'><div className='platform-panel-head'><div><div className='platform-eyebrow'>NEW KEY</div><h2>创建访问密钥</h2></div></div><form className='platform-form key-form' onSubmit={create}><label>密钥名称<input value={name} onChange={(e) => setName(e.target.value)} maxLength='30' required placeholder='例如：生产环境' /></label><label>累计积分预算<input type='number' min='0.000001' step='0.000001' value={budget} onChange={(e) => setBudget(e.target.value)} required /></label><label>有效期<select value={expiryDays} onChange={(e) => setExpiryDays(e.target.value)}><option value='-1'>永不过期</option><option value='7'>7 天</option><option value='30'>30 天</option><option value='90'>90 天</option></select></label>{allowed.length > 0 && <label className='form-wide'>模型范围<select multiple value={models} onChange={(e) => setModels(Array.from(e.target.selectedOptions, (option) => option.value))}>{allowed.map((item) => <option key={item} value={item}>{item}</option>)}</select><small>不选择模型时沿用服务端默认限制。</small></label>}<div className='form-wide platform-form-footer'><span>预算为此密钥累计可消耗上限；状态和有效期可以随时调整。</span><button className='platform-button primary' disabled={busy || !name.trim()}>{busy ? '正在创建…' : '创建密钥'}</button></div></form>{createdKey && <div className='created-key'><div><strong>请立即复制完整密钥</strong><span>离开此页面后，历史密钥不会再次展示。</span></div><code>{createdKey}</code><button className='platform-button secondary' onClick={async () => (await copy(createdKey)) ? showSuccess('已复制') : showError('复制失败')}>复制</button></div>}</section>
    <section className='platform-panel'><div className='platform-panel-head'><div><div className='platform-eyebrow'>YOUR KEYS</div><h2>已创建的密钥</h2></div></div>{loading ? <LoadingState /> : failed ? <ErrorState title='访问密钥暂时无法读取' /> : tokens.length ? <div className='platform-table-wrap'><table className='platform-table'><thead><tr><th>名称</th><th>密钥</th><th>有效期与状态</th><th>积分预算</th><th></th></tr></thead><tbody>{tokens.map((token) => <KeyRow key={token.id} token={token} onBudget={setTokenBudget} onSettings={setTokenSettings} onRevoke={revoke} />)}</tbody></table></div> : <EmptyState title='尚未创建访问密钥' detail='创建密钥并设置预算后，即可通过兼容 OpenAI 的 API 发起请求。' />}</section>
  </main>;
}

function KeyRow({ token, onBudget, onSettings, onRevoke }) {
  const [value, setValue] = useState('');
  const [expiry, setExpiry] = useState('keep'); const [status, setStatus] = useState(token.status === 2 ? 2 : 1);
  useEffect(() => { setExpiry('keep'); setStatus(token.status === 2 ? 2 : 1); }, [token.expired_time, token.status]);
  const saveSettings = () => { const expiredAt = expiry === 'keep' ? token.expired_time : expiry === 'never' ? -1 : Math.floor(Date.now() / 1000) + Number(expiry) * 86400; onSettings(token.id, expiredAt, Number(status)); };
  return <tr><td><strong>{token.name}</strong></td><td><code>••••••••</code></td><td><div className='key-settings'><span className={`state-chip ${token.status === 1 ? 'state-settled' : 'state-released'}`}>{token.status === 1 ? '可用' : token.status === 3 ? '已过期' : '已停用'}</span><select aria-label='密钥状态' value={status} onChange={(e) => setStatus(Number(e.target.value))}><option value={1}>启用</option><option value={2}>停用</option></select><select aria-label='密钥有效期' value={expiry} onChange={(e) => setExpiry(e.target.value)}><option value='keep'>保持当前有效期</option><option value='never'>永不过期</option>{[7,30,90].map((days) => <option key={days} value={days}>{days} 天后过期</option>)}</select><button className='text-button' onClick={saveSettings}>保存</button></div>{token.expired_time === -1 ? <small>当前永不过期</small> : <small>当前过期：{formatDate(token.expired_time * 1000)}</small>}</td><td><div className='token-budget-summary'>{token.has_budget ? token.unlimited ? '不限额' : `${formatPoints(token.limit_micro)} 积分 · 已用 ${formatPoints(token.spent_micro)} · 冻结 ${formatPoints(token.held_micro)}` : '尚未设置'}<div className='inline-budget'><input aria-label='积分预算' type='number' min='0.000001' step='0.000001' placeholder='新预算' value={value} onChange={(e) => setValue(e.target.value)} /><button className='text-button' onClick={() => value && onBudget(token.id, value)}>保存</button></div></div></td><td><button className='text-button danger' onClick={() => onRevoke(token.id)}>撤销</button></td></tr>;
}

export function ProfilePage() {
  const [profile, setProfile] = useState(null); const [password, setPassword] = useState(''); const [loading, setLoading] = useState(true); const [busy, setBusy] = useState(false);
  useEffect(() => { API.get('/api/user/self').then((res) => setProfile(res?.data?.data || null)).catch(() => showError('个人资料暂时无法加载')).finally(() => setLoading(false)); }, []);
  const save = async (event) => { event.preventDefault(); if (!profile) return; setBusy(true); try { const res = await API.put('/api/user/self', { username: profile.username, display_name: profile.display_name, password }); if (res?.data?.success) { showSuccess('个人资料已更新'); setPassword(''); } else showError(res?.data?.message || '保存失败'); } catch (_) { showError('保存失败'); } finally { setBusy(false); } };
  return <main className='platform-page'><PageTitle eyebrow='PROFILE' title='个人资料' intro='更新账户名称与登录密码。账户身份由 Miraphant 现有登录系统维护。' action={<Link to='/console/profile/bindings' className='platform-button secondary'>邮箱与第三方绑定</Link>} />{loading ? <LoadingState /> : profile ? <div className='platform-panel profile-panel'><form className='platform-form' onSubmit={save}><label>用户名<input value={profile.username || ''} onChange={(e) => setProfile({ ...profile, username: e.target.value })} required /></label><label>显示名称<input value={profile.display_name || ''} onChange={(e) => setProfile({ ...profile, display_name: e.target.value })} /></label><label className='form-wide'>新密码<input type='password' autoComplete='new-password' value={password} onChange={(e) => setPassword(e.target.value)} placeholder='留空表示不修改密码' /></label><div className='form-wide platform-form-footer'><span>密码不会回显。绑定邮箱或第三方账号请打开绑定设置。</span><button className='platform-button primary' disabled={busy}>{busy ? '保存中…' : '保存资料'}</button></div></form></div> : <ErrorState title='无法读取个人资料' />}</main>;
}

export function HelpPage() {
  return <main className='platform-page'><PageTitle eyebrow='HELP CENTER' title='接入与积分说明' intro='通过兼容 OpenAI 的 Chat Completions 接口接入文本模型。' />
    <div className='help-grid'><section className='platform-panel'><div className='platform-eyebrow'>QUICK START</div><h2>发送第一条请求</h2><pre><code>{`curl ${window.location.origin}/v1/chat/completions \\
  -H "Authorization: Bearer YOUR_ACCESS_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{"model":"MODEL_ID","messages":[{"role":"user","content":"Hello"}]}'`}</code></pre><p>请在价格页选择当前已发布模型，并在密钥管理中为密钥设置积分预算。</p></section><section className='platform-panel'><div className='platform-eyebrow'>BILLING</div><h2>积分如何计量</h2><ul><li>请求发送前会按输入估算与最大输出预留积分。</li><li>最终扣费只接受上游提供的完整可信用量，不以本地估算代替。</li><li>缺少用量或连接中断时，请求会进入待核实，冻结余额不会自动释放。</li><li>生效价格按价格版本记录；缓存输入与推理 token 在账单中分别标记。</li></ul><Link className='platform-text-link' to='/pricing'>查看生效价格 →</Link></section></div>
    <div className='platform-alert platform-alert-info'><strong>支付与退款</strong><span>微信支付、支付宝和退款流程尚未开通。当前没有真实充值入口；客服联系方式由平台配置后显示。</span></div>
  </main>;
}

export function AdminHome() {
  const [prices, setPrices] = useState([]); const [pending, setPending] = useState([]); const [loading, setLoading] = useState(true); const [failed, setFailed] = useState(false);
  useEffect(() => { Promise.all([API.get('/api/points/prices'), API.get('/api/admin/points/pending?limit=100')]).then(([priceRes, pendingRes]) => { setPrices(priceRes?.data?.prices || []); setPending(pendingRes?.data?.holds || []); }).catch(() => setFailed(true)).finally(() => setLoading(false)); }, []);
  const cards = [['模型定价', '/admin/pricing', `${prices.length} 个生效模型`], ['客户积分', '/admin/users', '客户查询与带原因赠送'], ['用量核实', '/admin/pending', `${pending.length} 条待核实`], ['渠道状态', '/admin/channels', '管理模型接入'], ['套餐管理', '/admin/packages', '发布版本与查看历史'], ['支付设置', '/admin/payments', '渠道配置与开通准备'], ['充值订单', '/admin/orders', '筛选订单与查看核验记录'], ['退款工作台', '/admin/refunds', '按退款权限查看申请'], ['财务与对账权限', '/admin/refund-access', '平台管理员委派退款与对账能力'], ['支付对账', '/admin/reconciliation', '查看批次、差异和导入记录'], ['操作审计', '/admin/audit', '查看积分、套餐与核实操作'], ['品牌与状态', '/admin/brand', '查看运行状态与上游来源']];
  return <main className='platform-page'><PageTitle eyebrow='ADMIN' title='平台管理' intro='管理入口受服务端权限控制。积分调整和价格发布都会记录审计。' />{loading ? <LoadingState /> : failed ? <ErrorState title='管理数据暂时无法读取' detail='会话权限可能已变化。请重新登录后再试。' /> : <div className='admin-module-grid'>{cards.map(([title, path, detail]) => <Link to={path} className='admin-module' key={path}><span>{title}</span><strong>{detail}</strong><i>打开模块 →</i></Link>)}</div>}<div className='platform-alert platform-alert-info'><strong>退款处理说明</strong><span>退款申请、审核、单独提交和进度核实按当前账户能力开放。真实商户退款仍受服务端配置控制。</span></div></main>;
}

export function AdminPricingPage() {
  const [prices, setPrices] = useState([]); const [failed, setFailed] = useState(false); const [form, setForm] = useState({ model: '', version: '', input_points_per_1k: '', cached_input_points_per_1k: '', output_points_per_1k: '', extra_points: '0', source: '' }); const [busy, setBusy] = useState(false);
  const load = () => API.get('/api/points/prices').then((res) => { setFailed(false); setPrices(res?.data?.prices || []); }).catch(() => setFailed(true));
  useEffect(() => { load(); }, []);
  const update = (key, value) => setForm((old) => ({ ...old, [key]: value }));
  const publish = async (event) => { event.preventDefault(); setBusy(true); try { const csrf = await API.get('/api/points/csrf'); const res = await API.post('/api/admin/points/prices', form, { headers: { 'X-CSRF-Token': csrf?.data?.csrf_token } }); if (res?.data?.success) { showSuccess('价格版本已发布并生效'); setForm({ model: '', version: '', input_points_per_1k: '', cached_input_points_per_1k: '', output_points_per_1k: '', extra_points: '0', source: '' }); await load(); } else showError(res?.data?.error || '发布失败'); } catch (_) { showError('价格发布失败'); } finally { setBusy(false); } };
  return <main className='platform-page'><PageTitle eyebrow='ADMIN · PRICING' title='模型价格' intro='发布新版本会立即成为对应模型的生效价格；每个请求仍保存创建时的价格快照。' />
    <section className='platform-panel'><div className='platform-panel-head'><div><div className='platform-eyebrow'>ACTIVE VERSION</div><h2>当前生效价格</h2></div></div>{failed ? <ErrorState title='价格列表暂时无法读取' /> : <><div className='platform-table-wrap'><table className='platform-table'><thead><tr><th>模型</th><th>版本</th><th>输入 / 1K</th><th>缓存输入 / 1K</th><th>输出 / 1K</th></tr></thead><tbody>{prices.map((item) => <tr key={item.model}><td>{item.model}</td><td>{item.version}</td><td>{formatRate(item.input_micro_per_1k)}</td><td>{formatRate(item.cached_input_micro_per_1k)}</td><td>{formatRate(item.output_micro_per_1k)}</td></tr>)}</tbody></table></div>{!prices.length && <EmptyState title='暂无生效价格' detail='请先确认模型成本与服务状态，再发布价格版本。' />}</>}</section>
    <section className='platform-panel'><div className='platform-panel-head'><div><div className='platform-eyebrow'>PUBLISH</div><h2>发布价格版本</h2></div></div><form className='platform-form' onSubmit={publish}><label>公开模型 ID<input value={form.model} onChange={(e) => update('model', e.target.value)} required /></label><label>版本标识<input value={form.version} onChange={(e) => update('version', e.target.value)} required /></label><label>输入积分 / 1K<input type='number' min='0' step='0.000001' value={form.input_points_per_1k} onChange={(e) => update('input_points_per_1k', e.target.value)} required /></label><label>缓存输入积分 / 1K<input type='number' min='0' step='0.000001' value={form.cached_input_points_per_1k} onChange={(e) => update('cached_input_points_per_1k', e.target.value)} required /></label><label>输出积分 / 1K<input type='number' min='0' step='0.000001' value={form.output_points_per_1k} onChange={(e) => update('output_points_per_1k', e.target.value)} required /></label><label>附加积分<input type='number' min='0' step='0.000001' value={form.extra_points} onChange={(e) => update('extra_points', e.target.value)} required /></label><label className='form-wide'>成本依据 / 备注<input value={form.source} onChange={(e) => update('source', e.target.value)} required /></label><div className='form-wide platform-form-footer'><span>缓存输入单价不得高于普通输入单价，版本标识不可复用。</span><button className='platform-button primary' disabled={busy}>{busy ? '发布中…' : '发布并生效'}</button></div></form></section>
  </main>;
}

export function AdminUsersPage() {
  const [users, setUsers] = useState([]); const [usersFailed, setUsersFailed] = useState(false); const [grant, setGrant] = useState({ user_id: '', amount_points: '', business_key: '', reason: '' }); const [busy, setBusy] = useState(false);
  useEffect(() => { API.get('/api/user/?p=0').then((res) => setUsers(res?.data?.data || [])).catch(() => setUsersFailed(true)); }, []);
  const update = (key, value) => setGrant((old) => ({ ...old, [key]: value }));
  const submit = async (event) => { event.preventDefault(); setBusy(true); try { const csrf = await API.get('/api/points/csrf'); const payload = { ...grant, user_id: Number(grant.user_id) }; const res = await API.post('/api/admin/points/adjustments', payload, { headers: { 'X-CSRF-Token': csrf?.data?.csrf_token } }); if (res?.data?.success) { showSuccess('积分赠送已记账'); setGrant({ user_id: '', amount_points: '', business_key: '', reason: '' }); } else showError(res?.data?.error || '调整失败'); } catch (_) { showError('积分调整失败'); } finally { setBusy(false); } };
  return <main className='platform-page'><PageTitle eyebrow='ADMIN · CUSTOMERS' title='客户与积分' intro='客户列表仅用于识别账户。余额只能通过带原因、带唯一业务键的积分流水调整。' />
    <section className='platform-panel'><div className='platform-panel-head'><div><div className='platform-eyebrow'>CUSTOMERS</div><h2>客户账户</h2></div></div>{usersFailed ? <ErrorState title='客户列表无法读取' detail='请检查管理会话权限并重新登录。' /> : <div className='platform-table-wrap'><table className='platform-table'><thead><tr><th>账户 ID</th><th>用户名</th><th>显示名称</th><th>状态</th></tr></thead><tbody>{users.map((user) => <tr key={user.id}><td>{user.id}</td><td>{user.username}</td><td>{user.display_name || '—'}</td><td>{user.status === 1 ? '正常' : '受限'}</td></tr>)}</tbody></table></div>}</section>
    <section className='platform-panel'><div className='platform-panel-head'><div><div className='platform-eyebrow'>AUDITED ADJUSTMENT</div><h2>赠送积分</h2></div></div><form className='platform-form' onSubmit={submit}><label>客户 ID<input type='number' min='1' value={grant.user_id} onChange={(e) => update('user_id', e.target.value)} required /></label><label>赠送积分<input type='number' min='0.000001' step='0.000001' value={grant.amount_points} onChange={(e) => update('amount_points', e.target.value)} required /></label><label>唯一业务键<input value={grant.business_key} onChange={(e) => update('business_key', e.target.value)} required /></label><label>调整原因<input value={grant.reason} onChange={(e) => update('reason', e.target.value)} required /></label><div className='form-wide platform-form-footer'><span>重复提交同一业务键不会重复入账；参数冲突会被拒绝。</span><button className='platform-button primary' disabled={busy}>{busy ? '提交中…' : '记录赠送'}</button></div></form></section>
  </main>;
}

export function AdminPendingPage() {
  const [holds, setHolds] = useState([]); const [loading, setLoading] = useState(true); const [failed, setFailed] = useState(false);
  const load = () => { setLoading(true); setFailed(false); return API.get('/api/admin/points/pending?limit=100').then((res) => setHolds(res?.data?.holds || [])).catch(() => setFailed(true)).finally(() => setLoading(false)); };
  useEffect(() => { load(); }, []);
  return <main className='platform-page'><PageTitle eyebrow='ADMIN · REVIEW' title='待核实用量' intro='查看冻结中的请求。只有取得可靠上游证据后才能结算；不得按估算补扣。' />{loading ? <LoadingState /> : failed ? <ErrorState title='待核实列表无法读取' detail='会话权限可能已变化。请重新登录后再试。' /> : holds.length ? <div className='pending-list'>{holds.map((hold) => <PendingHold key={hold.id} hold={hold} onResolved={load} />)}</div> : <EmptyState title='没有待核实请求' detail='发生上游用量不完整或请求中断时，记录会进入此列表。' />}</main>;
}

function PendingHold({ hold, onResolved }) {
  const [action, setAction] = useState('release'); const [usage, setUsage] = useState('0'); const [reason, setReason] = useState(''); const [busy, setBusy] = useState(false);
  const resolve = async (event) => { event.preventDefault(); setBusy(true); try { const csrf = await API.get('/api/points/csrf'); const decision = `admin-${Date.now()}-${hold.id}`; const res = await API.post(`/api/admin/points/pending/${encodeURIComponent(hold.logical_request_key)}/resolve`, { decision_key: decision, action, usage_points: usage, reason }, { headers: { 'X-CSRF-Token': csrf?.data?.csrf_token } }); if (res?.data?.success) { showSuccess('审核结果已记录'); await onResolved(); } else showError(res?.data?.error || '审核失败'); } catch (_) { showError('审核失败'); } finally { setBusy(false); } };
  return <article className='platform-panel pending-hold'><div className='pending-hold-top'><div><span className={`state-chip state-${hold.state}`}>{stateLabel(hold.state)}</span><h2>{hold.logical_request_key}</h2><p>客户 {hold.user_id} · 密钥 {hold.token_id} · 预留 {formatPoints(hold.budget_micro)} 积分</p></div><span>{formatDate(hold.created_at)}</span></div><div className='pending-evidence'>{(hold.attempts || []).map((attempt) => <div key={attempt.attempt_key}><b>上游尝试 · {attempt.state}</b><span>用量来源：{attempt.usage_source || '未取得'}</span><span>上游请求 ID：{attempt.provider_request_id || '未取得'}</span><span>上游响应 ID：{attempt.provider_response_id || '未取得'}</span><span>权威用量：{attempt.usage_authoritative ? '是' : '否'}</span><span>输入 {attempt.prompt_tokens} · 缓存 {attempt.cached_prompt_tokens} · 输出 {attempt.completion_tokens} · 推理 {attempt.reasoning_tokens}</span></div>)}</div><form className='platform-form pending-form' onSubmit={resolve}><label>处理方式<select value={action} onChange={(e) => setAction(e.target.value)}><option value='release'>确认未产生费用并释放冻结</option><option value='settle'>依据核实用量结算</option></select></label>{action === 'settle' && <label>确认用量积分<input type='number' min='0' step='0.000001' value={usage} onChange={(e) => setUsage(e.target.value)} required /></label>}<label className='form-wide'>核实依据<input value={reason} onChange={(e) => setReason(e.target.value)} required /></label><div className='form-wide platform-form-footer'><span>决定会写入不可变审计记录。业务键对应请求 ID，不可作为客户输入。</span><button className='platform-button secondary' disabled={busy}>{busy ? '记录中…' : '记录裁决'}</button></div></form></article>;
}

export function UnavailableAdminPage({ title = '模块准备中', detail = '该模块尚未连接已审核的服务端流程，当前不提供操作。' }) {
  return <main className='platform-page'><PageTitle eyebrow='ADMIN' title={title} intro={detail} /><div className='platform-alert platform-alert-info'><strong>当前状态：未开放</strong><span>页面不会创建模拟数据或显示虚构的订单、退款与支付状态。可返回管理首页查看已经接通的模块。</span></div><Link className='platform-button secondary' to='/admin'>返回管理首页</Link></main>;
}

export function NotFoundPage() { return <main className='platform-page not-found-page'><div className='platform-eyebrow'>404 · PAGE NOT FOUND</div><h1>这个页面不存在</h1><p>检查地址是否正确，或从控制台继续。</p><div className='platform-actions'><Link className='platform-button primary' to='/'>返回首页</Link><Link className='platform-button secondary' to='/console'>打开控制台</Link></div></main>; }

export function AdminRoute({ children }) {
  const [access, setAccess] = useState('loading');
  const [userState, userDispatch] = useContext(UserContext);
  useEffect(() => {
    API.get('/api/user/self').then((res) => {
      const data = res?.data?.data;
      if (!data) { setAccess('denied'); return; }
      const current = { id: Number(data.id), username: data.username || '', display_name: data.display_name || '', role: Number(data.role), status: Number(data.status) };
      localStorage.setItem('user', JSON.stringify(current));
      userDispatch({ type: 'login', payload: current });
      setAccess(current.role >= 10 ? 'allowed' : 'denied');
    }).catch(() => setAccess('denied'));
  }, [userDispatch]);
  if (access === 'loading') return <div className='platform-state'>正在核验管理权限…</div>;
  const user = userState.user || userFromStorage();
  return access === 'allowed' && user?.role >= 10 ? children : <Navigate to='/console' replace />;
}
export function CustomerRoute({ children }) { return userFromStorage() ? children : <NavigateToLogin />; }
function NavigateToLogin() { const location = window.location; const path = `${location.pathname}${location.search}`; return <LinkToLogin path={path} />; }
function LinkToLogin({ path }) { const navigate = useNavigate(); useEffect(() => navigate('/login', { replace: true, state: { from: path } }), [navigate, path]); return <div className='platform-state'>正在前往登录…</div>; }

export function LegacyRedirect({ to }) { return <LegacyPath to={to} />; }
function LegacyPath({ to }) { const navigate = useNavigate(); useEffect(() => { const user = userFromStorage(); const target = typeof to === 'function' ? to(user) : to; navigate(target || '/login', { replace: true }); }, [navigate, to]); return <div className='platform-state'>正在打开对应页面…</div>; }
