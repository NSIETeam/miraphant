import React, { useCallback, useEffect, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { StrictAPI as API } from '../../helpers';

const statusText = (value) => value === true ? '已启用' : value === false ? '未启用' : '服务端未提供';

export function BrandStatusPage() {
  const [data, setData] = useState(null);
  const [state, setState] = useState('loading');
  const [error, setError] = useState('');
  const navigate = useNavigate();
  const load = useCallback(async () => {
    setState('loading'); setError('');
    try {
      const response = await API.get('/api/status');
      const status = response?.data?.data;
      if (!response?.data?.success || !status || typeof status.points_billing_enabled !== 'boolean') throw new Error('状态信息不完整');
      setData(status); setState('ready');
    } catch (requestError) {
      if (requestError?.response?.status === 401) {
        localStorage.removeItem('user');
        navigate('/login', { replace: true, state: { from: '/admin/brand' } });
        return;
      }
      setError(requestError?.response?.status === 403 ? '当前账户无权读取平台状态。' : '暂时无法读取服务端状态，请稍后重试。');
      setState('error');
    }
  }, [navigate]);
  useEffect(() => { load(); }, [load]);

  return <main className='platform-page brand-status-page'>
    <div className='platform-title-row'><div><div className='platform-eyebrow'>PLATFORM STATUS</div><h1>Miraphant 品牌与状态</h1><p>此页展示应用提供的运行信息。名称、标志与主题由应用统一维护。</p></div><button className='platform-button secondary' onClick={load} disabled={state === 'loading'}>刷新状态</button></div>
    {state === 'loading' && <div className='platform-state'>正在读取服务端状态…</div>}
    {state === 'error' && <div className='platform-alert platform-alert-error' role='alert'><strong>状态暂不可用</strong><span>{error}</span><button className='platform-button secondary' onClick={load}>重试</button></div>}
    {state === 'ready' && <>
      <section className='platform-panel brand-overview'>
        <div className='brand-mark-card'><img src='/miraphant.svg' alt='Miraphant 标志' /><strong>Miraphant</strong><span>模型服务平台</span></div>
        <div className='brand-status-grid'>
          <div><span>应用版本</span><strong>{data.version || '未知'}</strong></div>
          <div><span>主题</span><strong>{data.theme === 'default' ? 'Miraphant 默认主题' : data.theme || '未知'}</strong></div>
          <div><span>积分计费</span><strong>{statusText(data.points_billing_enabled)}</strong></div>
          <div><span>密码登录</span><strong>{statusText(data.password_login)}</strong></div>
          <div><span>新用户注册</span><strong>{statusText(data.registration_enabled)}</strong></div>
          <div><span>邮箱验证</span><strong>{statusText(data.email_verification)}</strong></div>
        </div>
      </section>
      <section className='platform-panel brand-provenance'>
        <div className='platform-eyebrow'>SOURCE & LICENSE</div><h2>上游来源与许可证</h2>
        <p>网关基于 One API v0.6.10 代码维护。上游 MIT 许可证及署名保留在发行源码中。</p>
        <div className='platform-actions'><a className='platform-button primary' href='https://github.com/NSIETeam/miraphant/releases' target='_blank' rel='noreferrer'>Miraphant 发行版</a><a className='platform-button secondary' href='https://github.com/songquanpeng/one-api' target='_blank' rel='noreferrer'>One API 上游项目</a><a className='platform-button secondary' href='https://github.com/songquanpeng/one-api/blob/3915ce9814b8261a1ab13ed93adec58b463cd75c/LICENSE' target='_blank' rel='noreferrer'>查看 MIT 许可证</a><a className='platform-button secondary' href='https://github.com/songquanpeng/one-api/releases' target='_blank' rel='noreferrer'>查看上游发行版</a></div>
      </section>
      <section className='brand-status-links'><Link className='admin-module' to='/admin/payments'><span>支付与开通</span><strong>查看服务端开通准备</strong><i>打开模块 →</i></Link><Link className='admin-module' to='/admin/reconciliation'><span>支付对账</span><strong>查看账单批次与差异</strong><i>打开模块 →</i></Link></section>
    </>}
  </main>;
}
