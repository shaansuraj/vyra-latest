import { FiActivity, FiChevronLeft, FiChevronRight, FiClock, FiFilter, FiInfo, FiMessageSquare, FiRefreshCcw } from "solid-icons/fi";
import { ArkSelect, type SelectItem } from "../ark/ArkSelect";
import { createMemo, createSignal, For, onCleanup, onMount, Show, untrack } from "solid-js";
import { formatDistanceToNowStrict } from "date-fns";
import {
  agents,
  connectAgentEventsWebSocket,
  requestAgentEvents,
  subscribeToAgentEvents,
  type AgentEvent,
} from "../shared";

function safeJsonStringify(value: unknown) {
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

// NOTE: This hook is used by Main.tsx.
// It must return: { showAgentPanel, setShowAgentPanel, selectedAgent, setSelectedAgent, Toggle, Comp, JsonDisplay }
export function useAgentPanel(serviceId?: string) {
  const [showAgentPanel, setShowAgentPanel] = createSignal(false);
  const [selectedAgent, setSelectedAgent] = createSignal<string | null>(null);
  const [events, setEvents] = createSignal<AgentEvent[]>([]);
  const [showJson, setShowJson] = createSignal(false);
  const [loadingHistory, setLoadingHistory] = createSignal(false);

  const seen = new Set<string>();

  const agentOptions = createMemo(() => {
    const list = agents() || [];
    const items: SelectItem[] = [{ value: "", label: "All agents" }];
    for (const a of list) items.push({ value: a.id, label: a.name });
    return items;
  });

  const filteredEvents = createMemo(() => {
    const list = events();
    const agentId = selectedAgent();
    return list.filter((e) => {
      if (serviceId && Array.isArray(e.service_ids) && !e.service_ids.includes(serviceId)) return false;
      if (agentId && e.agent_id !== agentId) return false;
      return true;
    });
  });

  const requestLatest = async () => {
    if (loadingHistory()) return;
    setLoadingHistory(true);
    try {
      // Server-side filtering by service_id is supported.
      requestAgentEvents({ service_id: serviceId, limit: 150 });
    } finally {
      setTimeout(() => setLoadingHistory(false), 250); // tiny UX buffer
    }
  };

  onMount(() => {
    // Ensure WS is connected (it will no-op if already connected)
    connectAgentEventsWebSocket();

    // Request initial history
    requestLatest();

    const unsub = subscribeToAgentEvents((ev) => {
      if (serviceId && Array.isArray(ev.service_ids) && !ev.service_ids.includes(serviceId)) return;
      if (seen.has(ev.id)) return;
      seen.add(ev.id);

      // Prepend newest
      setEvents((prev) => {
        const next = [ev, ...prev];
        // Keep bounded list
        return next.slice(0, 300);
      });
    });

    onCleanup(() => {
      unsub();
    });
  });

  const Toggle = () => (
    <button
      class="btn-secondary flex items-center gap-2"
      onClick={() => setShowAgentPanel((v) => !v)}
      title="Toggle agent panel"
    >
      <FiActivity />
      <span class="hidden sm:inline">Agent Panel</span>
      <Show when={!showAgentPanel()}>
        <FiChevronLeft />
      </Show>
      <Show when={showAgentPanel()}>
        <FiChevronRight />
      </Show>
    </button>
  );

  const Comp = () => (
    <Show when={showAgentPanel()}>
      <div class="w-[380px] max-w-full h-full border-l border-neu-800 bg-neu-950/60 backdrop-blur p-4 flex flex-col">
        <div class="flex items-center justify-between gap-2">
          <div class="flex items-center gap-2">
            <FiActivity class="text-neu-200" />
            <div>
              <div class="font-semibold text-neu-100">Agent Events</div>
              <div class="text-xs text-neu-400">
                {serviceId ? `Camera: ${serviceId}` : "All cameras"}
              </div>
            </div>
          </div>

          <div class="flex items-center gap-2">
            <button
              class="btn-secondary px-2 py-1"
              onClick={() => requestLatest()}
              title="Refresh"
            >
              <FiRefreshCcw />
            </button>
            <button
              class="btn-secondary px-2 py-1"
              onClick={() => setShowJson((v) => !v)}
              title="Toggle JSON view"
            >
              <FiInfo />
            </button>
            <button
              class="btn-secondary px-2 py-1"
              onClick={() => setShowAgentPanel(false)}
              title="Close"
            >
              <FiChevronRight />
            </button>
          </div>
        </div>

        <div class="mt-3 flex items-center gap-2">
          <FiFilter class="text-neu-400" />
          <div class="flex-1">
            <ArkSelect
              items={agentOptions()}
              value={selectedAgent() ?? ""}
              onChange={(v) => setSelectedAgent(v || null)}
              placeholder="Filter by agent"
            />
          </div>
        </div>

        <div class="mt-3 flex-1 overflow-auto space-y-3 pr-1">
          <Show
            when={filteredEvents().length > 0}
            fallback={
              <div class="text-sm text-neu-400 mt-6">
                <div class="flex items-center gap-2">
                  <FiMessageSquare />
                  <span>No agent events yet.</span>
                </div>
                <div class="text-xs text-neu-500 mt-2">
                  Tip: Create an agent and bind it to this camera. Once frames are processed by the engine, events will appear here.
                </div>
              </div>
            }
          >
            <For each={filteredEvents()}>
              {(ev) => (
                <div class="rounded-lg border border-neu-800 bg-neu-900/40 p-3">
                  <div class="flex items-start justify-between gap-3">
                    <div>
                      <div class="text-sm font-semibold text-neu-100">{ev.agent_name}</div>
                      <div class="text-xs text-neu-400">Agent ID: {ev.agent_id}</div>
                    </div>
                    <div class="text-xs text-neu-400 flex items-center gap-1 whitespace-nowrap">
                      <FiClock />
                      {formatDistanceToNowStrict(new Date(ev.created_at), { addSuffix: true })}
                    </div>
                  </div>

                  <Show when={!showJson()}>
                    <div class="mt-2 text-sm text-neu-200 whitespace-pre-wrap">
                      {(() => {
                        const d = ev.data as any;
                        if (!d) return "";
                        // Prefer common fields if present
                        if (typeof d.summary === 'string') return d.summary;
                        if (typeof d.response === 'string') return d.response;
                        if (typeof d.message === 'string') return d.message;
                        return safeJsonStringify(d);
                      })()}
                    </div>
                  </Show>

                  <Show when={showJson()}>
                    <pre class="mt-2 text-xs text-neu-200 whitespace-pre-wrap overflow-auto max-h-64">{safeJsonStringify(ev)}</pre>
                  </Show>

                  <Show when={ev.metadata && Object.keys(ev.metadata || {}).length > 0}>
                    <div class="mt-2 text-xs text-neu-500">
                      <span class="font-medium text-neu-400">metadata:</span> {safeJsonStringify(ev.metadata)}
                    </div>
                  </Show>
                </div>
              )}
            </For>
          </Show>
        </div>

        <div class="mt-3 text-xs text-neu-500">
          <span class="text-neu-400">Loaded:</span> {filteredEvents().length} events
          <Show when={loadingHistory()}>
            <span class="ml-2 text-neu-400">(refreshing...)</span>
          </Show>
        </div>
      </div>
    </Show>
  );

  // Keep an exported JsonDisplay for compatibility (some code paths reference it)
  const JsonDisplay = () => (
    <Show when={showJson()}>
      <pre class="text-xs whitespace-pre-wrap">{safeJsonStringify(untrack(events))}</pre>
    </Show>
  );

  return {
    showAgentPanel,
    setShowAgentPanel,
    selectedAgent,
    setSelectedAgent,
    Toggle,
    Comp,
    JsonDisplay,
  };
}
