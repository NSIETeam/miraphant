import { API, showError } from '../helpers';

export function safeLocalPath(path, fallback = '/console') {
  if (typeof path !== 'string' || !path.startsWith('/') || path.startsWith('//') || /[\\\u0000-\u001f\u007f]/.test(path)) return fallback;
  try {
    const target = new URL(path, window.location.origin);
    if (target.origin !== window.location.origin) return fallback;
    return `${target.pathname}${target.search}${target.hash}`;
  } catch (_) {
    return fallback;
  }
}

export async function getOAuthState() {
  const res = await API.get('/api/oauth/state');
  const { success, message, data } = res.data;
  if (success) {
    return data;
  } else {
    showError(message);
    return '';
  }
}

export async function onGitHubOAuthClicked(github_client_id, returnPath = '/console') {
  const state = await getOAuthState();
  if (!state) return;
  localStorage.setItem('oauth_return_path', safeLocalPath(returnPath));
  const params = new URLSearchParams({ client_id: github_client_id, state, scope: 'user:email' });
  window.open(
    `https://github.com/login/oauth/authorize?${params.toString()}`
  );
}

export async function onLarkOAuthClicked(lark_client_id, returnPath = '/console') {
  const state = await getOAuthState();
  if (!state) return;
  localStorage.setItem('oauth_return_path', safeLocalPath(returnPath));
  let redirect_uri = `${window.location.origin}/oauth/lark`;
  window.open(
    `https://open.feishu.cn/open-apis/authen/v1/index?redirect_uri=${redirect_uri}&app_id=${lark_client_id}&state=${state}`
  );
}

export async function onOidcOAuthClicked(status, returnPath = '/console') {
  const state = await getOAuthState();
  if (!state || !status?.oidc_authorization_endpoint || !status?.oidc_client_id) return;
  localStorage.setItem('oauth_return_path', safeLocalPath(returnPath));
  const endpoint = new URL(status.oidc_authorization_endpoint, window.location.origin);
  if (!['http:', 'https:'].includes(endpoint.protocol)) return;
  endpoint.searchParams.set('client_id', status.oidc_client_id);
  endpoint.searchParams.set('redirect_uri', `${window.location.origin}/oauth/oidc`);
  endpoint.searchParams.set('response_type', 'code');
  endpoint.searchParams.set('scope', 'openid profile email');
  endpoint.searchParams.set('state', state);
  window.location.assign(endpoint.toString());
}
