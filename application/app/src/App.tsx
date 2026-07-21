import { lazy, onMount, onCleanup, createEffect, createSignal, Suspense, type Component, type ParentComponent } from 'solid-js';
import { authState, initAuth, fetchFlags, configState } from './shared';
import Authenticated from './components/Authenticated';

const Login = lazy(() => import('./pages/Login'));
const Register = lazy(() => import('./pages/Register'));
const Authorize = lazy(() => import('./pages/Authorize'));
const Main = lazy(() => import('./components/Main'));
const MarketingSite = lazy(() => import('./pages/MarketingSite'));

const AppLoading: Component = () => (
  <div class="min-h-screen bg-neu-950 text-neu-400 flex items-center justify-center text-sm">
    Loading VYRA...
  </div>
);

const LazyRoute: ParentComponent = (props) => (
  <Suspense fallback={<AppLoading />}>
    {props.children}
  </Suspense>
);

const MARKETING_HOST = 'vyracam.in';
const DASHBOARD_HOST = 'app.vyracam.in';

const isLocalHost = (host: string) =>
  host === 'localhost' ||
  host === '127.0.0.1' ||
  host === '[::1]' ||
  host.endsWith('.localhost');

const isMarketingHost = (host: string) => host === MARKETING_HOST || host === `www.${MARKETING_HOST}`;

const isAuthPath = (path: string) =>
  path === '/login' ||
  path === '/login.html' ||
  path === '/register' ||
  path === '/register.html' ||
  path === '/authorize' ||
  path === '/authorize.html';

const getDashboardBaseURL = () => {
  const host = window.location.hostname.toLowerCase();
  if (isLocalHost(host)) {
    const port = window.location.port ? `:${window.location.port}` : '';
    return `${window.location.protocol}//${host}${port}`;
  }
  return `https://${DASHBOARD_HOST}`;
};

// Simple router hook
const usePath = () => {
  const [path, setPath] = createSignal(window.location.pathname);

  onMount(() => {
    const handlePopState = () => setPath(window.location.pathname);
    window.addEventListener('popstate', handlePopState);

    onCleanup(() => {
      window.removeEventListener('popstate', handlePopState);
    });
  });

  return { path, setPath };
};

const App: Component = () => {
  const { path, setPath } = usePath();
  const host = () => window.location.hostname.toLowerCase();
  const onMarketingSite = () => isMarketingHost(host());
  const dashboardBaseURL = () => getDashboardBaseURL();

  const navigate = (to: string) => {
    if (window.location.pathname === to) return;
    window.history.pushState({}, '', to);
    setPath(window.location.pathname);
  };

  onMount(async () => {
    if (onMarketingSite()) return;

    // Fetch config first to determine dev mode
    await fetchFlags();
    console.log('[App] Config loaded - DEV_IMPERSONATE:', configState().DEV_IMPERSONATE);

    // Then init auth (will use impersonation header if configured)
    await initAuth();
    console.log('[App] After initAuth - isLoading:', authState().isLoading, 'isAuthenticated:', authState().isAuthenticated);
  });

  // Redirect to login if not authenticated (reactive)
  createEffect(() => {
    const currentPath = path();
    const isAuthPage = isAuthPath(currentPath);

    if (onMarketingSite()) {
      if (isAuthPage) {
        window.location.href = `${dashboardBaseURL()}${currentPath}`;
      }
      return;
    }

    if (!isAuthPage && !authState().isLoading && !authState().isAuthenticated) {
      console.log('[App] Redirecting to /login (path:', currentPath, ')');
      window.location.href = '/login';
    }
  });

  // Show auth pages if not authenticated
  const showLoginPage = () => path() === '/login' || path() === '/login.html';
  const showRegisterPage = () => path() === '/register' || path() === '/register.html';
  const showAuthorizePage = () => path() === '/authorize' || path() === '/authorize.html';

  if (onMarketingSite()) {
    return <LazyRoute><MarketingSite path={path} onNavigate={navigate} dashboardBaseURL={dashboardBaseURL()} /></LazyRoute>;
  }

  console.log('[App] Rendering - path:', path(), 'isLoading:', authState().isLoading, 'isAuthenticated:', authState().isAuthenticated);

  if (showAuthorizePage()) {
    console.log('[App] Showing authorize page');
    return <LazyRoute><Authorize /></LazyRoute>;
  }

  if (showLoginPage()) {
    console.log('[App] Showing login page');
    return <LazyRoute><Login /></LazyRoute>;
  }

  if (showRegisterPage()) {
    console.log('[App] Showing register page');
    return <LazyRoute><Register /></LazyRoute>;
  }

  console.log('[App] Showing authenticated app');
  return (
    <Authenticated>
      <LazyRoute><Main /></LazyRoute>
    </Authenticated>
  );
};

export default App;
