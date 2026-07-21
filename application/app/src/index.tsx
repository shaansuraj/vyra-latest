/* @refresh reload */
import './index.css';
import { render } from 'solid-js/web';

import App from './App';
import { initPostHog } from './lib/posthog';

if (import.meta.env.DEV) {
  void import('solid-devtools');
}

// Initialize PostHog early
initPostHog();

const root = document.getElementById('root');

if (import.meta.env.DEV && !(root instanceof HTMLElement)) {
  throw new Error(
    'Root element not found. Did you forget to add it to your index.html? Or maybe the id attribute got misspelled?',
  );
}

render(() => <App />, root!);
