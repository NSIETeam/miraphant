import React from 'react';
import { Link } from 'react-router-dom';

const OtherSetting = () => (
  <section className='brand-settings-summary'>
    <h3>Miraphant 品牌与版本</h3>
    <p>平台名称、标志与默认主题由应用统一维护；旧公告、首页内容和关于编辑项已停用，历史配置保留但不再参与当前页面展示。</p>
    <p>网关基于 One API v0.6.10 上游代码维护。保留其 MIT 许可证与来源说明。</p>
    <div className='brand-settings-links'>
      <Link className='platform-button secondary' to='/admin/brand'>查看平台状态</Link>
      <a className='platform-button secondary' href='https://github.com/NSIETeam/miraphant/releases' target='_blank' rel='noreferrer'>查看 Miraphant 发行版</a>
      <a className='platform-text-link' href='https://github.com/songquanpeng/one-api/releases' target='_blank' rel='noreferrer'>查看 One API 上游发行版</a>
      <a className='platform-text-link' href='https://github.com/songquanpeng/one-api/blob/3915ce9814b8261a1ab13ed93adec58b463cd75c/LICENSE' target='_blank' rel='noreferrer'>One API MIT 许可证</a>
    </div>
  </section>
);

export default OtherSetting;
