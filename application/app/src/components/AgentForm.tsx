import { ArkTabs } from "../ark/ArkTabs";
import { For, Show, createSignal, onMount, Setter } from "solid-js";
import { FiSettings, FiVideo } from "solid-icons/fi";
import { nodes, fetchNodes } from "../shared";
import ArkSwitch from "../ark/ArkSwitch";

type AgentMode = "ppe" | "person" | "situational";

type AgentTemplate = {
  name: string;
  mode: AgentMode;
  instruction: string;
  workerId: string;
};

const AGENT_TEMPLATES: AgentTemplate[] = [
  {
    name: "PPE Detection",
    mode: "ppe",
    instruction:
      "Detect PPE compliance. Identify missing or present equipment (hardhat, safety vest, gloves, mask, goggles). Report violations and compliant persons.",
    workerId: "vyra/ppe_detection",
  },
  {
    name: "Person Detection",
    mode: "person",
    instruction:
      "Detect and track persons with persistent IDs across cameras. Use shared face recognition for known and unknown identities, save visit snapshots, and keep the summary concise.",
    workerId: "vyra/person_detection",
  },
  {
    name: "Situational Awareness",
    mode: "situational",
    instruction:
      "Monitor camera frames with VLM at reduced FPS. Save only problematic frames and return a concise severity brief.",
    workerId: "vyra/vlm",
  },
];

interface ServiceWithNode {
  id: string;
  name: string;
  type: string;
  nodeId: string;
  nodeName: string | null;
}

/**
 * Drop-in safety: allow props like workerId to be passed either as:
 * - an accessor: workerId={() => "..."} or workerId={workerId} from createSignal
 * - a raw value: workerId={"vyra/vlm"} (common mistake: workerId={workerId()})
 */
type Accessor<T> = () => T;
type MaybeAccessor<T> = T | Accessor<T>;

function asAccessor<T>(v: MaybeAccessor<T>): Accessor<T> {
  return typeof v === "function" ? (v as Accessor<T>) : () => v as T;
}

function modeFromWorkerId(workerId: string): AgentMode {
  const wid = (workerId || "").toLowerCase();
  if (wid.includes("ppe") || wid.includes("helmet")) return "ppe";
  if (wid.includes("person_detection") || wid.includes("object_detection") || wid.includes("face")) return "person";
  return "situational";
}

export function AgentForm(props: {
  name: MaybeAccessor<string>;
  setName: (name: string) => void;

  instruction: MaybeAccessor<string>;
  setInstruction: (instruction: string) => void;

  workerId?: MaybeAccessor<string>;
  setWorkerId?: (workerId: string) => void;

  selectedServiceIds: MaybeAccessor<string[]>;
  setSelectedServiceIds: (ids: string[]) => void;

  showTemplates?: boolean;
  activeTab?: MaybeAccessor<string>;
  setActiveTab?: Setter<string>;
}) {
  const name = asAccessor(props.name);
  const instruction = asAccessor(props.instruction);
  const workerId = asAccessor(props.workerId ?? "");
  const selectedServiceIds = asAccessor(props.selectedServiceIds);

  const [localActiveTab, setLocalActiveTab] = createSignal("config");
  const activeTab = props.activeTab ? asAccessor(props.activeTab) : localActiveTab;
  const setActiveTab = props.setActiveTab ?? setLocalActiveTab;

  // keep a UI-level "mode" selector (maps to workerId + default instruction)
  const [mode, setMode] = createSignal<AgentMode>(modeFromWorkerId(workerId()));

  const allServices = () => {
    const result: ServiceWithNode[] = [];
    for (const node of nodes()) {
      for (const service of node.services) {
        result.push({
          id: service.id,
          name: service.name,
          type: service.type,
          nodeId: node.id,
          nodeName: node.name ?? null,
        });
      }
    }
    return result;
  };

  onMount(() => {
    fetchNodes();
    // ensure mode reflects initial worker selection
    setMode(modeFromWorkerId(workerId()));
  });

  const applyTemplate = (template: AgentTemplate) => {
    props.setName(template.name);
    props.setInstruction(template.instruction);
    props.setWorkerId?.(template.workerId);
    setMode(template.mode);
  };

  const applyMode = (m: AgentMode) => {
    setMode(m);

    if (m === "ppe") {
      props.setWorkerId?.("vyra/ppe_detection");
      if (!instruction().trim()) {
        props.setInstruction(
          "Detect PPE compliance. Identify missing or present equipment (hardhat, safety vest, gloves, mask, goggles). Report violations and compliant persons."
        );
      }
      if (!name().trim()) props.setName("PPE Detection");
      return;
    }

    if (m === "person") {
      props.setWorkerId?.("vyra/person_detection");
      if (!instruction().trim()) {
        props.setInstruction(
          "Detect and track persons with persistent IDs across cameras. Use shared face recognition for known and unknown identities, save visit snapshots, and keep the summary concise."
        );
      }
      if (!name().trim()) props.setName("Person Detection");
      return;
    }

    // situational awareness
    props.setWorkerId?.("vyra/vlm");
    if (!instruction().trim()) {
      props.setInstruction(
        "Monitor camera frames with VLM at reduced FPS. Save only problematic frames and return a concise severity brief."
      );
    }
    if (!name().trim()) props.setName("Situational Awareness");
  };

  return (
    <ArkTabs
      indicatorPosition="bottom"
      items={[
        {
          value: "config",
          label: "Agent Config",
          icon: <FiSettings class="w-4 h-4" />,
          content: (
            <div class="space-y-4 mt-4">
              <Show when={props.showTemplates}>
                <div>
                  <label class="text-sm font-medium text-neu-300 block mb-2">Templates</label>
                  <div class="flex gap-2">
                    <For each={AGENT_TEMPLATES}>
                      {(template) => (
                        <button
                          onClick={() => applyTemplate(template)}
                          class="text-xs flex-1 px-3 py-2 rounded-lg bg-neu-850 border border-neu-750 text-neu-300 hover:bg-neu-800 hover:border-neu-700 transition-colors truncate"
                        >
                          {template.name}
                        </button>
                      )}
                    </For>
                  </div>
                </div>
              </Show>

              <div>
                <label for="agent-name" class="text-sm font-medium text-neu-300">
                  Agent Name
                </label>
                <input
                  value={name()}
                  onInput={(e) => props.setName(e.currentTarget.value)}
                  placeholder="My Agent"
                  type="text"
                  id="agent-name"
                  class="px-4 py-2 mt-1 block w-full rounded-lg bg-neu-850 border border-neu-750 text-white focus:outline-none placeholder:text-neu-500"
                />
              </div>

              <div>
                <label for="agent-mode" class="text-sm font-medium text-neu-300">
                  Agent Type
                </label>
                <select
                  id="agent-mode"
                  value={mode()}
                  onChange={(e) => applyMode(e.currentTarget.value as AgentMode)}
                  class="px-4 py-2 mt-1 block w-full rounded-lg bg-neu-850 border border-neu-750 text-white focus:outline-none"
                >
                  <option value="ppe">PPE Detection</option>
                  <option value="person">Person Detection</option>
                  <option value="situational">Situational Awareness</option>
                </select>
                <p class="text-xs text-neu-500 mt-1">
                  PPE and Person Detection render <span class="text-neu-300">realtime boxes</span>. Situational Awareness uses{" "}
                  <span class="text-neu-300">VLM</span> at reduced FPS.
                </p>
              </div>

              {/* keep workerId visible (debug/advanced), but locked to the selected mode */}
              <div class="hidden">
                <select value={workerId()} onChange={(e) => props.setWorkerId?.(e.currentTarget.value)}>
                  <option value="vyra/vlm">vyra/vlm</option>
                  <option value="vyra/person_detection">vyra/person_detection</option>
                  <option value="vyra/ppe_detection">vyra/ppe_detection</option>
                </select>
              </div>

              <div>
                <label for="agent-instruction" class="text-sm font-medium text-neu-300">
                  Instruction
                </label>
                <textarea
                  value={instruction()}
                  onInput={(e) => props.setInstruction(e.currentTarget.value)}
                  placeholder="what happened in the video?"
                  id="agent-instruction"
                  class="min-h-32 px-4 py-2 mt-1 block w-full rounded-lg bg-neu-850 border border-neu-750 text-white focus:outline-none placeholder:text-neu-500 resize-none"
                  rows="3"
                />
              </div>
            </div>
          ),
        },
        {
          value: "services",
          label: "Services",
          icon: <FiVideo class="w-4 h-4" />,
          content: (
            <div class="space-y-4 mt-4">
              <div class="text-sm text-neu-400">
                Select which services this agent is allowed to read and monitor.
              </div>

              <Show
                when={allServices().length > 0}
                fallback={
                  <div class="text-center text-neu-500 py-8">
                    <p>No services available</p>
                    <p class="text-xs mt-1">Connect a node with services to get started</p>
                  </div>
                }
              >
                <div class="flex items-center justify-between mb-2">
                  <span class="text-xs text-neu-500">
                    {selectedServiceIds().length} of {allServices().length} selected
                  </span>

                  <button
                    onClick={() => {
                      const allSelected = selectedServiceIds().length === allServices().length;
                      if (allSelected) {
                        props.setSelectedServiceIds([]);
                      } else {
                        props.setSelectedServiceIds(allServices().map((s) => s.id));
                      }
                    }}
                    class="text-xs px-2 py-1 rounded-md bg-neu-850 border border-neu-750 text-neu-300 hover:bg-neu-800 hover:border-neu-700 transition-colors"
                  >
                    {selectedServiceIds().length === allServices().length ? "Deselect All" : "Select All"}
                  </button>
                </div>

                <div class="space-y-3 max-h-64 overflow-y-auto">
                  <For each={allServices()}>
                    {(service) => (
                      <div class="flex items-center justify-between py-2">
                        <p class="text-sm font-medium text-white truncate flex-1 mr-4">
                          {service.name || "Unnamed Service"}
                        </p>

                        <ArkSwitch
                          checked={() => selectedServiceIds().includes(service.id)}
                          onCheckedChange={(details) => {
                            if (details.checked) {
                              props.setSelectedServiceIds([...selectedServiceIds(), service.id]);
                            } else {
                              props.setSelectedServiceIds(selectedServiceIds().filter((id) => id !== service.id));
                            }
                          }}
                          label=""
                        />
                      </div>
                    )}
                  </For>
                </div>
              </Show>
            </div>
          ),
        },
      ]}
      value={activeTab()}
      onValueChange={(value) => setActiveTab(value)}
    />
  );
}
