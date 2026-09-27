import React, { lazy, Suspense, useCallback, useContext, useEffect } from 'react';
import { Route, Routes } from 'react-router-dom';
import Loading from './components/Loading';
import RegisterForm from './components/RegisterForm';
import LoginForm from './components/LoginForm';
import Setting from './pages/Setting';
import EditUser from './pages/User/EditUser';
import AddUser from './pages/User/AddUser';
import { API, showError, showNotice } from './helpers';
import PasswordResetForm from './components/PasswordResetForm';
import GitHubOAuth from './components/GitHubOAuth';
import PasswordResetConfirm from './components/PasswordResetConfirm';
import { UserContext } from './context/User';
import { StatusContext } from './context/Status';
import Channel from './pages/Channel';
import EditChannel from './pages/Channel/EditChannel';
import LarkOAuth from './components/LarkOAuth';
import PersonalSetting from './components/PersonalSetting';
import {
  AdminHome, AdminPendingPage, AdminPricingPage, AdminRoute, AdminUsersPage,
  AdminOrdersPage, AdminOrderDetailPage, AdminAuditPage,
  AdminPackagesPage, AdminPaymentsPage, OrderDetailPage, OrdersPage,
  ConsolePage, CustomerRoute, HelpPage, KeysPage, LegacyRedirect, NotFoundPage,
  PlatformHome, PricingPage, ProfilePage, UnavailableAdminPage, UsagePage,
  WalletPage, AdminRefundsPage, AdminRefundDetailPage, RefundCapabilityManagementPage,
  RefundCapabilityRoute,
} from './pages/Platform';

const About = lazy(() => import('./pages/About'));

function App() {
  const [, userDispatch] = useContext(UserContext);
  const [, statusDispatch] = useContext(StatusContext);

  const loadUser = useCallback(() => {
    let user = localStorage.getItem('user');
    if (user) {
      try {
        let data = JSON.parse(user);
        if (data && Number.isFinite(Number(data.id)) && Number.isFinite(Number(data.role))) userDispatch({ type: 'login', payload: data });
        else localStorage.removeItem('user');
      } catch (_) {
        localStorage.removeItem('user');
        userDispatch({ type: 'logout' });
      }
    }
  }, [userDispatch]);
  const loadStatus = useCallback(async () => {
    try {
      const res = await API.get('/api/status');
      if (!res?.data?.success || !res?.data?.data) {
        showError('无法正常连接至服务器！');
        return;
      }
      const data = res.data.data;
      localStorage.setItem('status', JSON.stringify(data));
      statusDispatch({ type: 'set', payload: data });
      localStorage.setItem('system_name', data.system_name || 'Miraphant');
      localStorage.setItem('logo', data.logo || '/miraphant.svg');
      localStorage.setItem('footer_html', data.footer_html || '');
      localStorage.setItem('quota_per_unit', data.quota_per_unit);
      localStorage.setItem('display_in_currency', data.display_in_currency);
      if (data.chat_link) localStorage.setItem('chat_link', data.chat_link);
      else localStorage.removeItem('chat_link');
      if (data.version !== process.env.REACT_APP_VERSION && data.version !== 'v0.0.0' && process.env.REACT_APP_VERSION !== '') {
        showNotice(`新版本可用：${data.version}，请刷新页面`);
      }
    } catch (_) {
      showError('无法正常连接至服务器！');
    }
  }, [statusDispatch]);

  useEffect(() => {
    loadUser();
    loadStatus();
    document.title = 'Miraphant 模型平台';
    const linkElement = document.querySelector("link[rel~='icon']");
    if (linkElement) linkElement.href = '/miraphant.svg';
  }, [loadStatus, loadUser]);

  return (
    <Routes>
      <Route path='/' element={<PlatformHome />} />
      <Route path='/pricing' element={<PricingPage />} />
      <Route path='/help' element={<HelpPage />} />
      <Route path='/console' element={<CustomerRoute><ConsolePage /></CustomerRoute>} />
      <Route path='/console/wallet' element={<CustomerRoute><WalletPage /></CustomerRoute>} />
      <Route path='/console/usage' element={<CustomerRoute><UsagePage /></CustomerRoute>} />
      <Route path='/console/keys' element={<CustomerRoute><KeysPage /></CustomerRoute>} />
      <Route path='/console/profile' element={<CustomerRoute><ProfilePage /></CustomerRoute>} />
      <Route path='/console/profile/bindings' element={<CustomerRoute><div className='platform-page'><PersonalSetting hideAccountDeletion /></div></CustomerRoute>} />
      <Route path='/checkout/:orderId' element={<CustomerRoute><OrderDetailPage checkoutMode /></CustomerRoute>} />
      <Route path='/console/orders' element={<CustomerRoute><OrdersPage /></CustomerRoute>} />
      <Route path='/console/orders/:orderId' element={<CustomerRoute><OrderDetailPage /></CustomerRoute>} />
      <Route path='/admin' element={<AdminRoute><AdminHome /></AdminRoute>} />
      <Route path='/admin/pricing' element={<AdminRoute><AdminPricingPage /></AdminRoute>} />
      <Route path='/admin/users' element={<AdminRoute><AdminUsersPage /></AdminRoute>} />
      <Route path='/admin/pending' element={<AdminRoute><AdminPendingPage /></AdminRoute>} />
      <Route path='/admin/channels' element={<AdminRoute><Channel /></AdminRoute>} />
      <Route path='/admin/channels/edit/:id' element={<AdminRoute><EditChannel /></AdminRoute>} />
      <Route path='/admin/channels/add' element={<AdminRoute><EditChannel /></AdminRoute>} />
      <Route path='/admin/packages' element={<AdminRoute><AdminPackagesPage /></AdminRoute>} />
      <Route path='/admin/payments' element={<AdminRoute><AdminPaymentsPage /></AdminRoute>} />
      <Route path='/admin/orders/:orderId' element={<AdminRoute><AdminOrderDetailPage /></AdminRoute>} />
      <Route path='/admin/orders' element={<AdminRoute><AdminOrdersPage /></AdminRoute>} />
      <Route path='/admin/refunds' element={<RefundCapabilityRoute anyOf={['refund.read']}><AdminRefundsPage /></RefundCapabilityRoute>} />
      <Route path='/admin/refunds/:refundKey' element={<RefundCapabilityRoute anyOf={['refund.read']}><AdminRefundDetailPage /></RefundCapabilityRoute>} />
      <Route path='/admin/refund-access' element={<RefundCapabilityRoute anyOf={['capability.manage']}><RefundCapabilityManagementPage /></RefundCapabilityRoute>} />
      <Route path='/admin/reconciliation' element={<AdminRoute><UnavailableAdminPage title='支付对账' /></AdminRoute>} />
      <Route path='/admin/audit' element={<AdminRoute><AdminAuditPage /></AdminRoute>} />
      <Route path='/admin/brand' element={<AdminRoute><UnavailableAdminPage title='品牌设置' detail='平台名称与主题已统一为 Miraphant，客服联系方式可在支付与客服模块接通后配置。' /></AdminRoute>} />
      <Route path='/admin/setting' element={<AdminRoute><Setting /></AdminRoute>} />
      <Route path='/admin/users/edit/:id' element={<AdminRoute><EditUser /></AdminRoute>} />
      <Route path='/admin/users/add' element={<AdminRoute><AddUser /></AdminRoute>} />
      <Route
        path='/channel'
        element={
          <AdminRoute>
            <Channel />
          </AdminRoute>
        }
      />
      <Route
        path='/channel/edit/:id'
        element={
          <Suspense fallback={<Loading></Loading>}>
            <AdminRoute><EditChannel /></AdminRoute>
          </Suspense>
        }
      />
      <Route
        path='/channel/add'
        element={
          <Suspense fallback={<Loading></Loading>}>
            <AdminRoute><EditChannel /></AdminRoute>
          </Suspense>
        }
      />
      <Route
        path='/token'
        element={
          <LegacyRedirect to='/console/keys' />
        }
      />
      <Route
        path='/token/edit/:id'
        element={
          <Suspense fallback={<Loading></Loading>}>
            <LegacyRedirect to='/console/keys' />
          </Suspense>
        }
      />
      <Route
        path='/token/add'
        element={
          <Suspense fallback={<Loading></Loading>}>
            <LegacyRedirect to='/console/keys' />
          </Suspense>
        }
      />
      <Route
        path='/redemption'
        element={
          <AdminRoute><UnavailableAdminPage title='兑换码管理' detail='旧版兑换入口尚未迁移到积分账本，当前已关闭。' /></AdminRoute>
        }
      />
      <Route
        path='/redemption/edit/:id'
        element={
          <Suspense fallback={<Loading></Loading>}>
            <AdminRoute><UnavailableAdminPage title='兑换码管理' detail='旧版兑换入口尚未迁移到积分账本，当前已关闭。' /></AdminRoute>
          </Suspense>
        }
      />
      <Route
        path='/redemption/add'
        element={
          <Suspense fallback={<Loading></Loading>}>
            <AdminRoute><UnavailableAdminPage title='兑换码管理' detail='旧版兑换入口尚未迁移到积分账本，当前已关闭。' /></AdminRoute>
          </Suspense>
        }
      />
      <Route
        path='/user'
        element={
          <AdminRoute><AdminUsersPage /></AdminRoute>
        }
      />
      <Route
        path='/user/edit/:id'
        element={
          <Suspense fallback={<Loading></Loading>}>
            <AdminRoute><EditUser /></AdminRoute>
          </Suspense>
        }
      />
      <Route
        path='/user/edit'
        element={
          <Suspense fallback={<Loading></Loading>}>
            <LegacyRedirect to='/console/profile' />
          </Suspense>
        }
      />
      <Route
        path='/user/add'
        element={
          <Suspense fallback={<Loading></Loading>}>
            <AdminRoute><AddUser /></AdminRoute>
          </Suspense>
        }
      />
      <Route
        path='/user/reset'
        element={
          <Suspense fallback={<Loading></Loading>}>
            <PasswordResetConfirm />
          </Suspense>
        }
      />
      <Route
        path='/login'
        element={
          <Suspense fallback={<Loading></Loading>}>
            <LoginForm />
          </Suspense>
        }
      />
      <Route
        path='/register'
        element={
          <Suspense fallback={<Loading></Loading>}>
            <RegisterForm />
          </Suspense>
        }
      />
      <Route
        path='/reset'
        element={
          <Suspense fallback={<Loading></Loading>}>
            <PasswordResetForm />
          </Suspense>
        }
      />
      <Route
        path='/oauth/github'
        element={
          <Suspense fallback={<Loading></Loading>}>
            <GitHubOAuth />
          </Suspense>
        }
      />
      <Route
        path='/oauth/lark'
        element={
          <Suspense fallback={<Loading></Loading>}>
            <LarkOAuth />
          </Suspense>
        }
      />
      <Route path='/oauth/oidc' element={<Suspense fallback={<Loading />}><GitHubOAuth provider='oidc' /></Suspense>} />
      <Route
        path='/setting'
        element={
          <AdminRoute>
            <Suspense fallback={<Loading></Loading>}>
              <Setting />
            </Suspense>
          </AdminRoute>
        }
      />
      <Route
        path='/topup'
        element={
        <LegacyRedirect to='/console/wallet' />
        }
      />
      <Route
        path='/log'
        element={
          <CustomerRoute><UsagePage /></CustomerRoute>
        }
      />
      <Route
        path='/about'
        element={
          <Suspense fallback={<Loading></Loading>}>
            <About />
          </Suspense>
        }
      />
      <Route
        path='/chat'
        element={
          <HelpPage />
        }
      />
      <Route path='/token/*' element={<LegacyRedirect to='/console/keys' />} />
      <Route path='/topup/*' element={<LegacyRedirect to='/console/wallet' />} />
      <Route path='/log/*' element={<CustomerRoute><UsagePage /></CustomerRoute>} />
      <Route path='/detail/*' element={<CustomerRoute><UsagePage /></CustomerRoute>} />
      <Route path='/user/edit/*' element={<LegacyRedirect to='/console/profile' />} />
      <Route path='/user/*' element={<AdminRoute><AdminUsersPage /></AdminRoute>} />
      <Route path='/setting/*' element={<AdminRoute><Setting /></AdminRoute>} />
      <Route path='/redemption/*' element={<AdminRoute><UnavailableAdminPage title='兑换码管理' detail='旧版兑换入口尚未迁移到积分账本，当前已关闭。' /></AdminRoute>} />
      <Route path='/panel' element={<LegacyRedirect to={(user) => user && user.role >= 10 ? '/admin' : '/console'} />} />
      <Route path='/panel/dashboard' element={<LegacyRedirect to={(user) => user && user.role >= 10 ? '/admin' : '/console'} />} />
      <Route path='/panel/profile' element={<LegacyRedirect to='/console/profile' />} />
      <Route path='/panel/topup' element={<LegacyRedirect to='/console/wallet' />} />
      <Route path='/panel/log' element={<LegacyRedirect to='/console/usage' />} />
      <Route path='/panel/token' element={<LegacyRedirect to='/console/keys' />} />
      <Route path='/panel/channel' element={<LegacyRedirect to='/admin/channels' />} />
      <Route path='/panel/user' element={<LegacyRedirect to='/admin/users' />} />
      <Route path='/panel/setting' element={<LegacyRedirect to='/admin/setting' />} />
      <Route path='/panel/*' element={<LegacyRedirect to={(user) => user && user.role >= 10 ? '/admin' : '/console'} />} />
      <Route path='*' element={<NotFoundPage />} />
    </Routes>
  );
}

export default App;
