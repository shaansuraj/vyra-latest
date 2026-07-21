import { createSignal, onMount, createEffect, Show, type Component } from 'solid-js';
import { register, authState, relay, configState } from '../shared';
import AuthFrame from '../components/AuthFrame';

/* ── Inline SVG Icons ────────────────────────────────────── */
const IconUser = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
    <path d="M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2" />
    <circle cx="12" cy="7" r="4" />
  </svg>
);

const IconAtSign = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
    <circle cx="12" cy="12" r="4" />
    <path d="M16 8v5a3 3 0 0 0 6 0v-1a10 10 0 1 0-4 8" />
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

const IconShieldCheck = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
    <path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z" />
    <path d="m9 12 2 2 4-4" />
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

const IconCheck = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
    <polyline points="20 6 9 17 4 12" />
  </svg>
);

const IconAlertCircle = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
    <circle cx="12" cy="12" r="10" />
    <line x1="12" y1="8" x2="12" y2="12" />
    <line x1="12" y1="16" x2="12.01" y2="16" />
  </svg>
);

/* ── Password strength logic ─────────────────────────────── */
const calcStrength = (pw: string): number => {
  if (!pw) return 0;
  let score = 0;
  if (pw.length >= 8) score++;
  if (/[A-Z]/.test(pw)) score++;
  if (/[a-z]/.test(pw)) score++;
  if (/[0-9]/.test(pw)) score++;
  if (/[^A-Za-z0-9]/.test(pw)) score++;
  // Map 0-5 to 0-4
  if (score <= 1) return pw.length > 0 ? 1 : 0;
  if (score === 2) return 2;
  if (score === 3) return 3;
  return 4;
};

const STRENGTH_LABELS = ['', 'Weak', 'Fair', 'Good', 'Strong'];

/* ── Username validation ─────────────────────────────────── */
const isValidUsername = (u: string) => /^[a-zA-Z0-9_]{3,20}$/.test(u);

const Register: Component = () => {
  const [name, setName] = createSignal('');
  const [username, setUsername] = createSignal('');
  const [email, setEmail] = createSignal('');
  const [password, setPassword] = createSignal('');
  const [confirmPw, setConfirmPw] = createSignal('');
  const [error, setError] = createSignal('');
  const [loading, setLoading] = createSignal(false);
  const [redirect, setRedirect] = createSignal<string | null>(null);

  const [showPw, setShowPw] = createSignal(false);
  const [showConfirmPw, setShowConfirmPw] = createSignal(false);
  const [eyeBounce1, setEyeBounce1] = createSignal(false);
  const [eyeBounce2, setEyeBounce2] = createSignal(false);

  const [usernameStatus, setUsernameStatus] = createSignal<'idle' | 'checking' | 'available' | 'taken' | 'invalid'>('idle');
  const [showTakenDialog, setShowTakenDialog] = createSignal(false);

  // Debounced username check
  let usernameTimer: ReturnType<typeof setTimeout>;
  const checkUsername = (val: string) => {
    clearTimeout(usernameTimer);
    if (!val) { setUsernameStatus('idle'); return; }
    if (!isValidUsername(val)) { setUsernameStatus('invalid'); return; }
    setUsernameStatus('checking');
    usernameTimer = setTimeout(async () => {
      try {
        const res = await fetch(relay(`/auth/check-username?username=${encodeURIComponent(val)}`));
        if (!res.ok) {
          console.warn('[Auth] Username check failed:', res.status);
          setUsernameStatus('idle');
          return;
        }
        const data = await res.json();
        if (typeof data.available === 'boolean') {
          setUsernameStatus(data.available ? 'available' : 'taken');
        } else {
          setUsernameStatus('idle');
        }
      } catch (err) {
        console.warn('[Auth] Username check error:', err);
        setUsernameStatus('idle');
      }
    }, 400);
  };

  onMount(() => {
    const params = new URLSearchParams(window.location.search);
    setRedirect(params.get('redirect'));
  });

  createEffect(() => {
    if (!configState().isLoading && !configState().SIGNUP_ENABLED) {
      const url = new URL('/login', window.location.origin);
      url.searchParams.set('message', 'Sign up is currently disabled.');
      const r = redirect();
      if (r) url.searchParams.set('redirect', r);
      window.location.href = url.toString();
      return;
    }

    const auth = authState();
    if (!auth.isLoading && auth.isAuthenticated) {
      window.location.href = redirect() || '/';
    }
  });

  const loginHref = () => {
    const r = redirect();
    return r ? `/login?redirect=${encodeURIComponent(r)}` : '/login';
  };

  const pwMatch = () => confirmPw().length > 0 && password() === confirmPw();
  const pwMismatch = () => confirmPw().length > 0 && password() !== confirmPw();
  const strength = () => calcStrength(password());

  const handleSubmit = async (e: Event) => {
    e.preventDefault();
    setError('');

    if (!configState().SIGNUP_ENABLED) {
      setError('Sign up is currently disabled');
      return;
    }

    // Frontend validations
    if (username() && !isValidUsername(username())) {
      setError('Username must be 3-20 characters (letters, numbers, underscores)');
      return;
    }
    if (usernameStatus() === 'taken') {
      setShowTakenDialog(true);
      return;
    }
    if (password() !== confirmPw()) {
      setError('Passwords do not match');
      return;
    }
    if (password().length < 8) {
      setError('Password must be at least 8 characters');
      return;
    }

    setLoading(true);
    const result = await register(email(), password(), name(), username() || undefined);

    if (result.success) {
      const url = new URL('/login', window.location.origin);
      url.searchParams.set('message', 'Account created! Please sign in.');
      const r = redirect();
      if (r) url.searchParams.set('redirect', r);
      window.location.href = url.toString();
    } else {
      if (result.message?.includes('username') && result.message?.includes('taken')) {
        setShowTakenDialog(true);
      } else {
        setError(result.message || 'Registration failed');
      }
      setLoading(false);
    }
  };

  const togglePw = () => {
    setShowPw(!showPw());
    setEyeBounce1(true);
    setTimeout(() => setEyeBounce1(false), 220);
  };

  const toggleConfirmPw = () => {
    setShowConfirmPw(!showConfirmPw());
    setEyeBounce2(true);
    setTimeout(() => setEyeBounce2(false), 220);
  };

  return (
    <AuthFrame
      panelTitle="Create Account"
      panelSubtitle="Set up credentials for your operator workspace."
      footer={
        <div class="auth-switch" style={{ 'animation-delay': '1.3s' }}>
          Already have an account?{' '}
          <a href={loginHref()}>Login</a>
        </div>
      }
    >
      <Show when={error()}>
        <div class="auth-alert auth-alert-error">{error()}</div>
      </Show>

      {/* Username taken dialog */}
      <Show when={showTakenDialog()}>
        <div class="auth-dialog-overlay" onClick={() => setShowTakenDialog(false)}>
          <div class="auth-dialog" onClick={(e) => e.stopPropagation()}>
            <div class="auth-dialog-icon">
              <IconAlertCircle />
            </div>
            <div class="auth-dialog-title">Username Unavailable</div>
            <div class="auth-dialog-text">
              The username <strong style={{ color: '#f3f7ff' }}>"{username()}"</strong> is already in use by another account. Please choose a different username to continue.
            </div>
            <button class="auth-dialog-btn" onClick={() => setShowTakenDialog(false)}>
              Choose Another Username
            </button>
          </div>
        </div>
      </Show>

      <form onSubmit={handleSubmit} style={{ display: 'flex', 'flex-direction': 'column', gap: '18px' }}>
        {/* Full Name */}
        <div class="auth-field" style={{ 'animation-delay': '0.55s' }}>
          <label class="auth-field-label" for="name">Full Name</label>
          <div class="auth-input-wrap">
            <input
              type="text"
              id="name"
              placeholder="Jane Smith"
              autocomplete="name"
              class="auth-input"
              value={name()}
              onInput={(e) => setName(e.currentTarget.value)}
              required
            />
            <span class="auth-input-icon"><IconUser /></span>
          </div>
        </div>

        {/* Username */}
        <div
          class="auth-field"
          style={{ 'animation-delay': '0.63s' }}
          classList={{ 'is-error': usernameStatus() === 'taken' || usernameStatus() === 'invalid' }}
        >
          <label class="auth-field-label" for="username">Username</label>
          <div class="auth-input-wrap">
            <input
              type="text"
              id="username"
              placeholder="jane_smith"
              autocomplete="username"
              class="auth-input"
              classList={{ 'has-check': usernameStatus() === 'available' }}
              value={username()}
              onInput={(e) => {
                setUsername(e.currentTarget.value);
                checkUsername(e.currentTarget.value);
              }}
            />
            <span class="auth-input-icon"><IconAtSign /></span>
            <Show when={usernameStatus() === 'available'}>
              <span class="auth-input-check"><IconCheck /></span>
            </Show>
          </div>
          <Show when={usernameStatus() === 'invalid' && username().length > 0}>
            <div class="auth-field-error">3-20 characters: letters, numbers, underscores only</div>
          </Show>
          <Show when={usernameStatus() === 'taken'}>
            <div class="auth-field-error">This username is already taken</div>
          </Show>
          <Show when={usernameStatus() === 'checking'}>
            <div style={{ 'font-size': '11px', color: '#50627b', 'margin-top': '4px' }}>Checking availability...</div>
          </Show>
        </div>

        {/* Email */}
        <div class="auth-field" style={{ 'animation-delay': '0.71s' }}>
          <label class="auth-field-label" for="email">Email</label>
          <div class="auth-input-wrap">
            <input
              type="email"
              id="email"
              placeholder="you@example.com"
              autocomplete="email"
              class="auth-input"
              value={email()}
              onInput={(e) => setEmail(e.currentTarget.value)}
              required
            />
            <span class="auth-input-icon"><IconMail /></span>
          </div>
        </div>

        {/* Password */}
        <div class="auth-field" style={{ 'animation-delay': '0.79s' }}>
          <label class="auth-field-label" for="reg-password">Password</label>
          <div class="auth-input-wrap">
            <input
              type={showPw() ? 'text' : 'password'}
              id="reg-password"
              placeholder="••••••••"
              autocomplete="new-password"
              class="auth-input has-toggle"
              value={password()}
              onInput={(e) => setPassword(e.currentTarget.value)}
              required
            />
            <span class="auth-input-icon"><IconLock /></span>
            <button
              type="button"
              class="auth-eye-btn"
              classList={{ 'is-visible': showPw(), 'is-bouncing': eyeBounce1() }}
              onClick={togglePw}
              onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); togglePw(); } }}
              aria-label={showPw() ? 'Hide password' : 'Show password'}
              tabIndex={0}
            >
              {showPw() ? <IconEye /> : <IconEyeOff />}
            </button>
          </div>

          {/* Strength meter */}
          <Show when={password().length > 0}>
            <div class={`auth-strength level-${strength()}`}>
              <div class="auth-strength-seg" classList={{ filled: strength() >= 1 }} />
              <div class="auth-strength-seg" classList={{ filled: strength() >= 2 }} />
              <div class="auth-strength-seg" classList={{ filled: strength() >= 3 }} />
              <div class="auth-strength-seg" classList={{ filled: strength() >= 4 }} />
            </div>
            <div class={`auth-strength-label level-${strength()}`}>{STRENGTH_LABELS[strength()]}</div>
          </Show>
        </div>

        {/* Confirm Password */}
        <div
          class="auth-field"
          style={{ 'animation-delay': '0.87s' }}
          classList={{ 'is-error': pwMismatch() }}
        >
          <label class="auth-field-label" for="confirm-password">Confirm Password</label>
          <div class="auth-input-wrap">
            <input
              type={showConfirmPw() ? 'text' : 'password'}
              id="confirm-password"
              placeholder="••••••••"
              autocomplete="new-password"
              class="auth-input has-toggle"
              classList={{ 'has-check': pwMatch() }}
              value={confirmPw()}
              onInput={(e) => setConfirmPw(e.currentTarget.value)}
              required
            />
            <span class="auth-input-icon"><IconShieldCheck /></span>
            <button
              type="button"
              class="auth-eye-btn"
              classList={{ 'is-visible': showConfirmPw(), 'is-bouncing': eyeBounce2() }}
              onClick={toggleConfirmPw}
              onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); toggleConfirmPw(); } }}
              aria-label={showConfirmPw() ? 'Hide password' : 'Show password'}
              tabIndex={0}
            >
              {showConfirmPw() ? <IconEye /> : <IconEyeOff />}
            </button>
            <Show when={pwMatch()}>
              <span class="auth-input-check" style={{ right: '48px' }}><IconCheck /></span>
            </Show>
          </div>
          <Show when={pwMismatch()}>
            <div class="auth-field-error">Passwords do not match</div>
          </Show>
        </div>

        {/* Submit */}
        <button
          type="submit"
          disabled={loading()}
          class="auth-submit auth-field"
          classList={{ 'is-loading': loading() }}
          style={{ 'animation-delay': '0.95s', 'margin-top': '6px' }}
        >
          <span class="auth-submit-text">Create Account</span>
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

export default Register;
