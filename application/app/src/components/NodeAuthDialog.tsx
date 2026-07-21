import { Component, Show, createSignal, createEffect } from 'solid-js';
import { FiShield, FiX, FiCheck, FiAlertTriangle, FiChevronDown, FiChevronUp } from 'solid-icons/fi';
import { pendingNodeAuth, authorizeNode, dismissNodeAuth } from '../shared';
import { toaster } from '../ark/ArkToast';

const NodeAuthDialog: Component = () => {
  const [activeNodeId, setActiveNodeId] = createSignal<string | null>(null);
  const [authorizing, setAuthorizing] = createSignal(false);
  const [showAdvanced, setShowAdvanced] = createSignal(false);
  const [seen, setSeen] = createSignal<Set<string>>(new Set());

  createEffect(() => {
    const pending = pendingNodeAuth();
    if (!pending.length) {
      setActiveNodeId(null);
      return;
    }

    const first = pending[0];
    if (!first) return;

    if (!seen().has(first.node_id)) {
      setSeen((prev) => new Set([...prev, first.node_id]));
      toaster.create({
        title: 'Node authorization requested',
        description: `Node ${first.node_id.slice(0, 12)}... is waiting for approval.`,
      } as any);
    }

    if (!activeNodeId()) {
      setActiveNodeId(first.node_id);
    }
  });

  const req = () => pendingNodeAuth().find((r) => r.node_id === activeNodeId()) || pendingNodeAuth()[0];

  const closeModal = () => {
    setActiveNodeId(null);
    setShowAdvanced(false);
  };

  const handleAuthorize = async () => {
    const current = req();
    if (!current) return;
    setAuthorizing(true);
    const ok = await authorizeNode(current.node_id);
    setAuthorizing(false);
    if (ok) {
      toaster.success({ title: 'Node authorized', description: `Node ${current.node_id.slice(0, 12)}... is now authorized.` });
      dismissNodeAuth(current.node_id);
      closeModal();
    } else {
      toaster.error({ title: 'Authorization failed', description: 'Could not authorize this node.' });
    }
  };

  return (
    <Show when={req()}>
      {(currentReq) => (
        <div class="fixed inset-0 z-[100] bg-black/60 backdrop-blur-sm flex items-center justify-center p-4" onClick={closeModal}>
          <div class="w-full max-w-md bg-neu-900 border border-violet-700/40 rounded-2xl overflow-hidden" onClick={(e) => e.stopPropagation()}>
            <div class="px-5 py-4 border-b border-neu-800/70 flex items-center justify-between">
              <div class="flex items-center gap-3">
                <div class="w-9 h-9 rounded-xl bg-violet-500/20 border border-violet-500/30 flex items-center justify-center">
                  <FiShield size={16} class="text-violet-300" />
                </div>
                <div>
                  <div class="text-sm font-semibold text-white">Authorize Node</div>
                  <div class="text-xs text-neu-500">Approve relay access for this node</div>
                </div>
              </div>
              <button class="p-1 rounded hover:bg-neu-800 text-neu-500 hover:text-neu-200" onClick={closeModal}>
                <FiX size={14} />
              </button>
            </div>

            <div class="px-5 py-4 space-y-3">
              <div class="flex items-start gap-2">
                <FiAlertTriangle size={14} class="text-amber-400 mt-0.5" />
                <p class="text-xs text-neu-300 leading-relaxed">
                  Node <span class="font-mono text-neu-100">{currentReq().node_id}</span> requested authorization.
                  Approve to allow it to connect and stream assigned camera services.
                </p>
              </div>

              <button
                class="w-full flex items-center justify-between px-3 py-2 rounded-lg bg-neu-850 border border-neu-800 text-xs text-neu-400 hover:text-neu-200"
                onClick={() => setShowAdvanced(!showAdvanced())}
              >
                <span>Advanced details</span>
                <Show when={showAdvanced()} fallback={<FiChevronDown size={12} />}>
                  <FiChevronUp size={12} />
                </Show>
              </button>

              <Show when={showAdvanced()}>
                <div class="rounded-lg bg-neu-950 border border-neu-800 p-3">
                  <div class="text-[11px] text-neu-500 mb-1">Authorization URL (fallback)</div>
                  <div class="text-[11px] text-neu-300 break-all font-mono">{currentReq().auth_url}</div>
                </div>
              </Show>
            </div>

            <div class="px-5 py-4 border-t border-neu-800/70 flex items-center gap-2">
              <button
                class="flex-1 py-2 rounded-xl bg-violet-600 hover:bg-violet-500 text-white text-xs font-semibold disabled:opacity-60"
                disabled={authorizing()}
                onClick={handleAuthorize}
              >
                <Show when={!authorizing()} fallback={<span class="inline-flex items-center gap-2"><svg class="animate-spin w-3 h-3" viewBox="0 0 24 24" fill="none"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4" /><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8v8H4z" /></svg>Authorizing…</span>}>
                  <span class="inline-flex items-center gap-2"><FiCheck size={12} />Authorize Node</span>
                </Show>
              </button>
              <button
                class="px-3 py-2 rounded-xl bg-neu-800 hover:bg-neu-700 border border-neu-700 text-neu-300 text-xs"
                onClick={() => {
                  dismissNodeAuth(currentReq().node_id);
                  closeModal();
                }}
              >
                Dismiss
              </button>
            </div>
          </div>
        </div>
      )}
    </Show>
  );
};

export default NodeAuthDialog;
