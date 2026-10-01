import { showError } from './utils';
import axios from 'axios';

export const API = axios.create({
  baseURL: process.env.REACT_APP_SERVER ? process.env.REACT_APP_SERVER : '',
});

// New Miraphant pages need normal promise rejection handling so they can show
// explicit loading, empty, and unavailable states without treating failures as
// successful empty responses. Legacy One API pages keep their existing client.
export const StrictAPI = axios.create({
  baseURL: process.env.REACT_APP_SERVER ? process.env.REACT_APP_SERVER : '',
});

// Administrative application settings use a browser session, live root-role
// checks, and the same-origin token. Keep this behavior local to /api/option.
export async function updateOption(key, value) {
  const csrfResponse = await StrictAPI.get('/api/refund-auth/csrf');
  const csrfToken = csrfResponse?.data?.csrf_token;
  if (!csrfToken) throw new Error('安全验证暂不可用');
  return StrictAPI.put('/api/option/', { key, value }, {
    headers: { 'X-CSRF-Token': csrfToken },
  });
}

API.interceptors.response.use(
  (response) => response,
  (error) => {
    showError(error);
  }
);
