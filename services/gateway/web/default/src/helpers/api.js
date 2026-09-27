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

API.interceptors.response.use(
  (response) => response,
  (error) => {
    showError(error);
  }
);
