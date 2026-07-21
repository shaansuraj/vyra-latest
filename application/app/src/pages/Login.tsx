import { createSignal, onMount, createEffect, Show, type Component } from 'solid-js';
import { login, authState, configState, relayFetch } from '../shared';
import AuthFrame from '../components/AuthFrame';

/* ── Inline SVG Icons ────────────────────────────────────── */
const IconUser = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
    <path d="M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2" />
    <circle cx="12" cy="7" r="4" />
  </svg>
);

const IconMail = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
    <rect x="2" y="4" width="20" height="16" rx="2" />
    <path d="m22 7-8.97 5.7a1.94 1.94 0 0 1-2.06 0L2 7" />
  </svg>
);

const IconLock = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
    <rect x="3" y="11" width="18" height="11" rx="2" ry="2" />
    <path d="M7 11V7a5 5 0 0 1 10 0v4" />
  </svg>
);

const IconEye = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
    <path d="M2 12s3-7 10-7 10 7 10 7-3 7-10 7-10-7-10-7Z" />
    <circle cx="12" cy="12" r="3" />
  </svg>
);

const IconEyeOff = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
    <path d="M9.88 9.88a3 3 0 1 0 4.24 4.24" />
    <path d="M10.73 5.08A10.43 10.43 0 0 1 12 5c7 0 10 7 10 7a13.16 13.16 0 0 1-1.67 2.68" />
    <path d="M6.61 6.61A13.526 13.526 0 0 0 2 12s3 7 10 7a9.74 9.74 0 0 0 5.39-1.61" />
    <line x1="2" y1="2" x2="22" y2="22" />
  </svg>
);

const Login: Component = () => {
  const [identifier, setIdentifier] = createSignal('');
  const [password, setPassword] = createSignal('');
  const [error, setError] = createSignal('');
  const [success, setSuccess] = createSignal('');
  const [loading, setLoading] = createSignal(false);
  const [showPw, setShowPw] = createSignal(false);
  const [eyeBounce, setEyeBounce] = createSignal(false);
  const [signupDisabledPrompt, setSignupDisabledPrompt] = createSignal('');

  const isSimplifiedLogin = () => configState().DEV_IMPERSONATE === 'true';
  const isEmail = () => identifier().includes('@');

  onMount(() => {
    const params = new URLSearchParams(window.location.search);
    const msg = params.get('message');
    if (msg) setSuccess(msg);
  });

  createEffect(() => {
    const auth = authState();
    if (!auth.isLoading && auth.isAuthenticated) {
      window.location.href = redirectParam() || '/';
    }
  });

  const redirectParam = () => new URLSearchParams(window.location.search).get('redirect');

  const handleSubmit = async (e: Event) => {
    e.preventDefault();
    setError('');
    setLoading(true);

    if (isSimplifiedLogin()) {
      try {
        const response = await relayFetch('/auth/me', { headers: { 'X-Dev-Impersonate': identifier() } });
        if (response.ok) {
          const data = await response.json();
          if (data.success) {
            window.location.href = redirectParam() || '/';
          } else {
            setError('User not found');
            setLoading(false);
          }
        } else {
          setError('Authentication failed');
          setLoading(false);
        }
      } catch {
        setError('Network error');
        setLoading(false);
      }
    } else {
      // Send identifier as "email" — backend Login() handles both email and username
      const result = await login(identifier(), password());
      if (result.success) {
        window.location.href = redirectParam() || '/';
      } else {
        setError(result.message || 'Login failed');
        setLoading(false);
      }
    }
  };

  const registerHref = () => {
    const r = redirectParam();
    return r ? `/register?redirect=${encodeURIComponent(r)}` : '/register';
  };

  const handleSignupClick = (e: MouseEvent) => {
    if (configState().SIGNUP_ENABLED) return;
    e.preventDefault();
    setSignupDisabledPrompt('Signup is disabled by administrator.');
  };

  const togglePw = () => {
    setShowPw(!showPw());
    setEyeBounce(true);
    setTimeout(() => setEyeBounce(false), 220);
  };

  return (
    <AuthFrame
      panelTitle="Sign In"
      panelSubtitle="Welcome back to your command workspace."
      footer={
        <div class="auth-switch" style={{ 'animation-delay': '1.0s' }}>
          <Show when={!configState().isLoading}>
            Don't have an account?{' '}
            <a
              href={registerHref()}
              onClick={handleSignupClick}
              aria-disabled={!configState().SIGNUP_ENABLED}
              title={!configState().SIGNUP_ENABLED ? 'Signup is disabled' : undefined}
            >
              Sign Up
            </a>
          </Show>
        </div>
      }
    >
      <Show when={error()}>
        <div class="auth-alert auth-alert-error">{error()}</div>
      </Show>

      <Show when={success()}>
        <div class="auth-alert auth-alert-success">{success()}</div>
      </Show>

      <Show when={signupDisabledPrompt()}>
        <div class="auth-alert auth-alert-error">{signupDisabledPrompt()}</div>
      </Show>

      <form onSubmit={handleSubmit} style={{ display: 'flex', 'flex-direction': 'column', gap: '20px' }}>
        {/* Username or Email */}
        <div class="auth-field" style={{ 'animation-delay': '0.55s' }}>
          <label class="auth-field-label" for="identifier">Username or Email</label>
          <div class="auth-input-wrap">
            <input
              type="text"
              id="identifier"
              placeholder="username or you@example.com"
              autocomplete="username"
              class="auth-input"
              value={identifier()}
              onInput={(e) => setIdentifier(e.currentTarget.value)}
              required
            />
            <span class="auth-input-icon" style={{ opacity: isEmail() ? '0' : '1', transition: 'opacity 0.2s ease' }}>
              <IconUser />
            </span>
            <span
              class="auth-input-icon"
              style={{
                opacity: isEmail() ? '1' : '0',
                transition: 'opacity 0.2s ease',
                color: isEmail() ? '#00D4FF' : undefined,
              }}
            >
              <IconMail />
            </span>
          </div>
        </div>

        {/* Password */}
        <div
          class="auth-field"
          style={{ 'animation-delay': '0.65s' }}
          classList={{ hidden: isSimplifiedLogin() }}
        >
          <label class="auth-field-label" for="password">Password</label>
          <div class="auth-input-wrap">
            <input
              type={showPw() ? 'text' : 'password'}
              id="password"
              placeholder="••••••••"
              autocomplete="current-password"
              class="auth-input has-toggle"
              value={password()}
              onInput={(e) => setPassword(e.currentTarget.value)}
              required={!isSimplifiedLogin()}
            />
            <span class="auth-input-icon"><IconLock /></span>
            <button
              type="button"
              class="auth-eye-btn"
              classList={{ 'is-visible': showPw(), 'is-bouncing': eyeBounce() }}
              onClick={togglePw}
              onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); togglePw(); } }}
              aria-label={showPw() ? 'Hide password' : 'Show password'}
              tabIndex={0}
            >
              {showPw() ? <IconEye /> : <IconEyeOff />}
            </button>
          </div>
        </div>

        {/* Submit */}
        <button
          type="submit"
          disabled={loading()}
          class="auth-submit auth-field"
          classList={{ 'is-loading': loading() }}
          style={{ 'animation-delay': '0.75s', 'margin-top': '8px' }}
        >
          <span class="auth-submit-text">Sign In</span>
          <span class="auth-submit-spinner">
            <span class="auth-spinner-dot" />
            <span class="auth-spinner-dot" />
            <span class="auth-spinner-dot" />
          </span>
        </button>
      </form>
    </AuthFrame>
  );
};

export default Login;
