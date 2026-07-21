const POSTHOG_KEY = import.meta.env.VITE_POSTHOG_KEY;
const POSTHOG_HOST = import.meta.env.VITE_POSTHOG_HOST || 'https://us.i.posthog.com';

let clientPromise: Promise<any | null> | null = null;
let initialized = false;

const getPostHog = async () => {
  if (!POSTHOG_KEY || POSTHOG_KEY === '') {
    return null;
  }
  if (!clientPromise) {
    clientPromise = import('posthog-js').then((module) => {
      const client = module.default;
      if (!initialized) {
        client.init(POSTHOG_KEY, {
          api_host: POSTHOG_HOST,
          person_profiles: 'identified_only',
          capture_pageview: true,
          capture_pageleave: true,
          persistence: 'localStorage',
        });
        initialized = true;
        console.log('[PostHog] Initialized');
      }
      return client;
    }).catch((error) => {
      console.warn('[PostHog] Failed to load analytics SDK', error);
      return null;
    });
  }
  return clientPromise;
};

export const initPostHog = () => {
  if (!POSTHOG_KEY || POSTHOG_KEY === '') {
    console.log('[PostHog] No API key provided, skipping initialization');
    return;
  }

  void getPostHog();
};

const posthog = {
  identify: (...args: any[]) => {
    void getPostHog().then((client) => client?.identify?.(...args));
  },
  capture: (...args: any[]) => {
    void getPostHog().then((client) => client?.capture?.(...args));
  },
  reset: (...args: any[]) => {
    void getPostHog().then((client) => client?.reset?.(...args));
  },
};

export default posthog;
