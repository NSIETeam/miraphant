import React from 'react';
import { Link } from 'react-router-dom';

const Footer = () => <footer className='platform-footer'>
  <div className='platform-footer-inner'>
    <div className='footer-brand'><img src='/miraphant.svg' alt='' /><span>Miraphant · 统一模型服务与积分账户</span></div>
    <div className='footer-links'><Link to='/pricing'>价格</Link><Link to='/help'>接入帮助</Link><Link to='/about'>关于</Link></div>
    <div className='footer-license'>基于 <a href='https://github.com/songquanpeng/one-api' target='_blank' rel='noreferrer'>One API</a> 构建 · <a href='https://opensource.org/license/mit' target='_blank' rel='noreferrer'>MIT License</a></div>
  </div>
</footer>;

export default Footer;
