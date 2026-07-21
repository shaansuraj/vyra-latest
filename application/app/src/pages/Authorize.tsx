import { createEffect, createSignal, onMount, Show, type Component } from 'solid-js';
import { FaSolidCircleCheck } from 'solid-icons/fa';
import { authState, relayFetch } from '../shared';
import AuthFrame from '../components/AuthFrame';

const Authorize: Component = () => {
  const [nodeId, setNodeId] = createSignal('');
  const [loading, setLoading] = createSignal(false);
  const [error, setError] = createSignal('');
  const [success, setSuccess] = createSignal(false);

  const redirectToLogin = () => {
    const params = new URLSearchParams(window.location.search);
    const node = params.get('node');
    const dest = node ? `/authorize?node=${encodeURIComponent(node)}` : '/authorize';
    window.location.href = `/login?redirect=${encodeURIComponent(dest)}`;
  };

  onMount(() => {
    const params = new URLSearchParams(window.location.search);
    const node = params.get('node');
    if (!node) {
      setError('Missing node parameter.');
      return;
    }
    setNodeId(node);
  });

  createEffect(() => {
    const auth = authState();
    if (!auth.isLoading && !auth.isAuthenticated) {
      redirectToLogin();
    }
  });

  const handleAuthorize = async (e: Event) => {
    e.preventDefault();
    if (!nodeId()) return;

    setError('');
    setLoading(true);

    try {
      const response = await relayFetch('/api/authorize', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ node_id: nodeId() }),
      });

      const data = await response.json();
      if (response.ok && data.success) {
        setSuccess(true);
        setTimeout(() => {
          window.location.href = '/';
        }, 1800);
      } else {
        setError(data.message || 'Authorization failed');
      }
    } catch {
      setError('Network error. Please try again.');
    } finally {
      setLoading(false);
    }
  };

  if (authState().isLoading) {
    return (
      <div class="min-h-screen kinetic-shell flex items-center justify-center">
        <div class="depth-panel rounded-2xl px-6 py-5 text-neu-300">Checking authorization context…</div>
      </div>
    );
  }

  if (!authState().isAuthenticated) {
    return <div class="min-h-screen kinetic-shell" />;
  }

  return (
    <AuthFrame
      eyebrow="Node Pairing"
      title={<>Authorize relay access for this node</>}
      subtitle="This secure handshake grants the node permission to connect and publish camera services into the command center."
      bullets={[
        'Node identity is verified against your account context',
        'Authorization can be completed without leaving this screen',
        'Once approved, streams and services appear in Control and Command Center',
      ]}
      panelTitle="Authorize Node"
      panelSubtitle="Approve this node so it can join the workspace."
    >
      <Show when={success()} fallback={
        <form onSubmit={handleAuthorize} class="space-y-4">
          <div class="rounded-xl bg-neu-900/70 border border-neu-800/70 px-3 py-3">
            <div class="text-[11px] tracking-[0.1em] text-neu-500">Node ID</div>
            <div class="text-xs text-neu-200 mt-1 break-all font-mono">{nodeId() || 'Unavailable'}</div>
          </div>

          <Show when={error()}>
            <div class="bg-red-900/20 text-red-300 border border-red-800/60 p-3 rounded-xl text-sm">{error()}</div>
          </Show>

          <div class="text-xs text-neu-500">Logged in as {authState().user?.email || 'Unknown user'}</div>

          <button type="submit" class="w-full saas-cta justify-center py-2.5 magnetic-button mt-1" disabled={loading() || !nodeId()}>
            {loading() ? 'Authorizing…' : 'Authorize Node'}
          </button>
        </form>
      }>
        <div class="flex flex-col items-center gap-3 py-4 text-center">
          <FaSolidCircleCheck class="w-14 h-14 text-green-300" />
          <div class="text-lg font-semibold text-neu-100">Node Authorized</div>
          <div class="text-sm text-neu-500">Redirecting to dashboard…</div>
        </div>
      </Show>
    </AuthFrame>
  );
};

export default Authorize;
