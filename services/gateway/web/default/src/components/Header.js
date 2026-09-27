import React, { useContext, useState } from 'react';
import { Link, useLocation, useNavigate } from 'react-router-dom';
import { UserContext } from '../context/User';
import { API, showError } from '../helpers';

const publicLinks = [
  { label: '首页', to: '/' },
  { label: '模型价格', to: '/pricing' },
  { label: '接入帮助', to: '/help' },
];

const Header = () => {
  const [userState, userDispatch] = useContext(UserContext);
  const [menuOpen, setMenuOpen] = useState(false);
  const navigate = useNavigate();
  const location = useLocation();
  const user = userState.user || (() => { try { return JSON.parse(localStorage.getItem('user') || 'null'); } catch { return null; } })();
  const links = user
    ? [...publicLinks, { label: '控制台', to: '/console' }, { label: '积分与充值', to: '/console/wallet' }, { label: '访问密钥', to: '/console/keys' }, { label: '个人资料', to: '/console/profile' }, ...(user.role >= 10 ? [{ label: '管理', to: '/admin' }] : [])]
    : publicLinks;

  const logout = async () => {
    try { await API.get('/api/user/logout'); } catch (_) { showError('注销请求失败，请稍后重试'); }
    userDispatch({ type: 'logout' });
    localStorage.removeItem('user');
    setMenuOpen(false);
    navigate('/');
  };

  return <header className='platform-header'>
    <div className='platform-header-inner'>
      <Link className='brand-lockup' to='/' aria-label='Miraphant 首页'><img src='/miraphant.svg' alt='' /><span>Miraphant<small>模型平台</small></span></Link>
      <button className='mobile-menu-toggle' aria-label={menuOpen ? '关闭导航' : '打开导航'} onClick={() => setMenuOpen(!menuOpen)}>{menuOpen ? '×' : '☰'}</button>
      <nav className={`platform-nav ${menuOpen ? 'is-open' : ''}`}>
        {links.map((link) => <Link key={link.to} className={location.pathname === link.to || (link.to !== '/' && location.pathname.startsWith(`${link.to}/`)) ? 'active' : ''} to={link.to} onClick={() => setMenuOpen(false)}>{link.label}</Link>)}
        {user ? <div className='platform-account'><Link className='account-profile-link' to='/console/profile' onClick={() => setMenuOpen(false)} aria-label='个人资料'><span className='account-dot'>{(user.display_name || user.username || 'M').slice(0, 1).toUpperCase()}</span><span className='account-name'>{user.display_name || user.username}</span></Link><button onClick={logout}>退出</button></div> : <div className='platform-account'><Link className='header-login' to='/login' onClick={() => setMenuOpen(false)}>登录</Link><Link className='header-register' to='/register' onClick={() => setMenuOpen(false)}>注册账户</Link></div>}
      </nav>
    </div>
  </header>;
};

export default Header;
