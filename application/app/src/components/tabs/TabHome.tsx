import { For, Show, onMount, createSignal } from 'solid-js';
import { FiVideo, FiServer, FiEdit2, FiTrash2, FiCamera, FiPlus, FiPlay, FiSquare, FiRefreshCw } from 'solid-icons/fi';
import {
  nodes, nodesLoading, fetchNodes, setTab,
  relayFetch, type NodeService, type NodeInfo,
  cameras, camerasLoading, fetchCameras,
  createCamera, editCamera, deleteCamera, type CameraInfo,
  managedNodes, fetchManagedNodes,
  createManagedNode, renameManagedNode, deleteManagedNode,
  startManagedNode, stopManagedNode,
  assignCameraToNode, unassignCameraFromNode,
  systemStatus, fetchSystemStatus,
  fetchRecentAgentEventsRest, type AgentEvent,
} from '../../shared';
import { ArkDialog } from '../../ark/ArkDialog';
import { ArkTabs } from '../../ark/ArkTabs';
import { Dialog } from '@ark-ui/solid/dialog';
import { toaster } from '../../ark/ArkToast';

interface ServiceWithNode {
  service: NodeService;
  nodeId: string;
}

// ── Helpers ────────────────────────────────────────────────────────────────

function StatusDot(props: { running: boolean; online?: boolean }) {
  const color = () => {
    if (props.running && props.online) return 'bg-green-500';
    if (props.running) return 'bg-yellow-500';
    return 'bg-neu-500';
  };
  const label = () => {
    if (props.running && props.online) return 'Running & Online';
    if (props.running) return 'Starting…';
    return 'Stopped';
  };
  return (
    <span class="inline-flex items-center gap-1.5">
      <span class={`w-2 h-2 rounded-full ${color()}`}></span>
      <span class="text-xs text-neu-300">{label()}</span>
    </span>
  );
}

// ── Services View ──────────────────────────────────────────────────────────

function ServiceInfoDialog(props: { service: NodeService; nodeId: string }) {
  return (
    <ArkDialog
      trigger={(_, setOpen) => (
        <button
          onClick={(e) => { e.stopPropagation(); setOpen(true); }}
          class="px-3 py-1.5 rounded-lg border border-neu-750 bg-neu-800 hover:bg-neu-850 text-neu-300 text-xs font-medium transition-colors"
        >
          Detail
        </button>
      )}
      title="Service Details"
      description="View all information about this service"
    >
      <div class="mt-4 space-y-4">
        <div>
          <label class="text-xs font-medium text-neu-500 uppercase tracking-wide">Service ID</label>
          <p class="mt-1 text-sm text-white break-all font-mono">{props.service.id}</p>
        </div>
        <div>
          <label class="text-xs font-medium text-neu-500 uppercase tracking-wide">Service Name</label>
          <p class="mt-1 text-sm text-white">{props.service.name || 'Unnamed Service'}</p>
        </div>
        <div>
          <label class="text-xs font-medium text-neu-500 uppercase tracking-wide">Type</label>
          <p class="mt-1">
            <span class="bg-neu-800 text-neu-300 text-xs font-medium px-2.5 py-0.5 rounded">
              {props.service.type}
            </span>
          </p>
        </div>
        <div>
          <label class="text-xs font-medium text-neu-500 uppercase tracking-wide">Node ID</label>
          <p class="mt-1 text-sm text-white break-all font-mono">{props.nodeId}</p>
        </div>
        <div>
          <label class="text-xs font-medium text-neu-500 uppercase tracking-wide">Address</label>
          <p class="mt-1 text-sm text-white font-mono">{props.service.addr}:{props.service.port}{props.service.path}</p>
        </div>
      </div>
    </ArkDialog>
  );
}

export function ServicesView() {
  const allServices = (): ServiceWithNode[] => {
    const result: ServiceWithNode[] = [];
    for (const node of nodes()) {
      for (const service of node.services) {
        result.push({ service, nodeId: node.id });
      }
    }
    return result;
  };

  const handleServiceClick = (nodeId: string, serviceId: string, name: string) => {
    setTab({ type: 'view', nodeId, serviceId, name });
  };

  return (
    <div class="h-full min-h-0 flex flex-col bg-neu-925/20 rounded-2xl border border-neu-800/70 overflow-hidden">
      <div class="px-5 py-4 border-b border-neu-800/70 bg-neu-900/45">
        <h3 class="text-sm font-semibold text-neu-100">Live Services</h3>
        <p class="text-xs text-neu-500 mt-1">
          {allServices().length} service{allServices().length !== 1 ? 's' : ''} available. Click a row to open single-camera view.
        </p>
      </div>
      <Show
        when={allServices().length > 0}
        fallback={
          <div class="flex-1 min-h-0 flex items-center justify-center text-neu-500 px-6">
            <div class="saas-empty w-full max-w-xl">
              <FiVideo class="mx-auto mb-4 w-12 h-12" />
              <p>No services found</p>
              <p class="text-sm mt-1">Start a node and assign cameras to see streams here</p>
            </div>
          </div>
        }
      >
        <div class="flex-1 min-h-0 relative overflow-x-auto overflow-y-auto">
          <table class="data-table">
            <thead>
              <tr>
                <th scope="col" class="px-6 py-3 font-medium">Service Name</th>
                <th scope="col" class="px-6 py-3 font-medium">Type</th>
                <th scope="col" class="px-6 py-3 font-medium">Node ID</th>
                <th scope="col" class="px-6 py-3 font-medium">Address</th>
                <th scope="col" class="px-6 py-3 font-medium">Actions</th>
              </tr>
            </thead>
            <tbody>
              <For each={allServices()}>
                {({ service, nodeId }) => (
                  <tr
                    class="cursor-pointer transition-colors"
                    onClick={() => handleServiceClick(nodeId, service.id, service.name)}
                  >
                    <td class="px-6 py-4 font-medium text-white">{service.name || 'Unnamed Service'}</td>
                    <td class="px-6 py-4">
                      <span class="bg-neu-800 text-neu-300 text-xs font-medium px-2.5 py-0.5 rounded whitespace-nowrap">
                        {service.type}
                      </span>
                    </td>
                    <td class="px-6 py-4 max-w-[20vw]">
                      <span class="line-clamp-1 break-all text-xs">{nodeId}</span>
                    </td>
                    <td class="px-6 py-4">
                      <span class="text-xs">{service.addr}:{service.port}{service.path}</span>
                    </td>
                    <td class="px-6 py-4">
                      <ServiceInfoDialog service={service} nodeId={nodeId} />
                    </td>
                  </tr>
                )}
              </For>
            </tbody>
          </table>
        </div>
      </Show>
    </div>
  );
}

// ── Managed Nodes View ─────────────────────────────────────────────────────

function CreateNodeDialog() {
  const [name, setName] = createSignal('');

  const handleCreate = async (closeDialog: () => void) => {
    const n = name().trim();
    if (!n) return;
    toaster.promise(async () => {
      const node = await createManagedNode(n);
      if (!node) throw new Error('Failed to create node');
      setName('');
      closeDialog();
    }, {
      loading: { title: 'Creating...', description: 'Setting up node.' },
      success: { title: 'Created', description: 'Node created successfully.' },
      error: { title: 'Failed', description: 'Could not create node.' },
    });
  };

  return (
    <ArkDialog
      trigger={(_, setOpen) => (
        <button onClick={() => setOpen(true)} class="btn-primary flex items-center gap-1.5">
          <FiPlus class="w-4 h-4" />
          Create Node
        </button>
      )}
      title="Create Node"
      description="Create a new managed node. Assign cameras to it and click Start."
    >
      {(setOpen) => (
        <div class="mt-4 space-y-4">
          <div>
            <label class="text-sm font-medium text-neu-300">Node Name *</label>
            <input
              type="text"
              value={name()}
              onInput={(e) => setName(e.currentTarget.value)}
              placeholder="e.g. Warehouse Node"
              class="px-3 py-1.5 mt-1 block w-full rounded-lg bg-neu-850 border border-neu-750 text-white focus:outline-none placeholder:text-neu-500"
            />
          </div>
          <div class="flex justify-end pt-2">
            <button
              onClick={() => handleCreate(() => setOpen(false))}
              disabled={!name().trim()}
              class="btn-primary disabled:opacity-50"
            >
              Create Node
            </button>
          </div>
        </div>
      )}
    </ArkDialog>
  );
}

function RenameNodeDialog(props: { id: string; currentName: string }) {
  const [name, setName] = createSignal(props.currentName);

  const handleSave = async (closeDialog: () => void) => {
    const n = name().trim();
    if (!n) return;
    toaster.promise(async () => {
      const ok = await renameManagedNode(props.id, n);
      if (!ok) throw new Error('Failed to rename node');
      closeDialog();
    }, {
      loading: { title: 'Saving...', description: 'Renaming node.' },
      success: { title: 'Saved', description: 'Node renamed.' },
      error: { title: 'Failed', description: 'Could not rename node.' },
    });
  };

  return (
    <ArkDialog
      trigger={(_, setOpen) => (
        <button
          onClick={(e) => { e.stopPropagation(); setOpen(true); }}
          class="px-3 py-1.5 rounded-lg border border-neu-750 bg-neu-800 hover:bg-neu-850 text-neu-300 text-xs font-medium transition-colors flex items-center gap-1.5"
        >
          <FiEdit2 class="w-3 h-3" />
          Rename
        </button>
      )}
      title="Rename Node"
      description="Enter a new name for this node"
    >
      {(setOpen) => (
        <div class="mt-4 space-y-4">
          <div>
            <label class="text-sm font-medium text-neu-300">Node Name</label>
            <input
              type="text"
              value={name()}
              onInput={(e) => setName(e.currentTarget.value)}
              class="px-3 py-1.5 mt-1 block w-full rounded-lg bg-neu-850 border border-neu-750 text-white focus:outline-none"
            />
          </div>
          <div class="flex justify-end pt-2">
            <button
              onClick={() => handleSave(() => setOpen(false))}
              disabled={!name().trim()}
              class="btn-primary disabled:opacity-50"
            >
              Save
            </button>
          </div>
        </div>
      )}
    </ArkDialog>
  );
}

function DeleteNodeDialog(props: { id: string; name: string }) {
  const handleDelete = async (closeDialog: () => void) => {
    toaster.promise(async () => {
      const ok = await deleteManagedNode(props.id);
      if (!ok) throw new Error('Failed to delete node');
      closeDialog();
    }, {
      loading: { title: 'Deleting...', description: 'Removing node.' },
      success: { title: 'Deleted', description: 'Node deleted.' },
      error: { title: 'Failed', description: 'Could not delete node.' },
    });
  };

  return (
    <ArkDialog
      trigger={(_, setOpen) => (
        <button
          onClick={(e) => { e.stopPropagation(); setOpen(true); }}
          class="px-3 py-1.5 rounded-lg border border-neu-750 bg-neu-800 hover:bg-neu-850 text-neu-300 text-xs font-medium transition-colors flex items-center gap-1.5"
        >
          <FiTrash2 class="w-3 h-3" />
          Delete
        </button>
      )}
      title="Delete Node"
      description={`Are you sure you want to delete "${props.name}"? This will stop the node process.`}
    >
      {(setOpen) => (
        <div class="flex justify-end pt-4">
          <button onClick={() => handleDelete(() => setOpen(false))} class="btn-danger">
            Delete Node
          </button>
        </div>
      )}
    </ArkDialog>
  );
}

function NodeCameraAssignDialog(props: { nodeID: string; nodeName: string }) {
  const [selectedCameraIDs, setSelectedCameraIDs] = createSignal<string[]>([]);
  const [initialCameraIDs, setInitialCameraIDs] = createSignal<string[]>([]);

  const isAssigned = (camera: CameraInfo) => {
    const nodeIDs = camera.node_ids && camera.node_ids.length > 0
      ? camera.node_ids
      : (camera.managed_node_id ? [camera.managed_node_id] : []);
    return nodeIDs.includes(props.nodeID);
  };

  const prepareOpen = (setOpen: (open: boolean) => void) => {
    const assigned = cameras().filter(isAssigned).map(c => c.id);
    setInitialCameraIDs(assigned);
    setSelectedCameraIDs(assigned);
    setOpen(true);
  };

  const toggleCamera = (cameraID: string, checked: boolean) => {
    if (checked) {
      setSelectedCameraIDs(prev => prev.includes(cameraID) ? prev : [...prev, cameraID]);
    } else {
      setSelectedCameraIDs(prev => prev.filter(id => id !== cameraID));
    }
  };

  const handleSave = (closeDialog: () => void) => {
    toaster.promise(async () => {
      const before = new Set(initialCameraIDs());
      const after = new Set(selectedCameraIDs());
      const failedCameras: string[] = [];

      for (const camera of cameras()) {
        const had = before.has(camera.id);
        const hasNow = after.has(camera.id);
        if (!had && hasNow) {
          const ok = await assignCameraToNode(props.nodeID, camera.id);
          if (!ok) failedCameras.push(camera.name || camera.id);
        }
        if (had && !hasNow) {
          const ok = await unassignCameraFromNode(props.nodeID, camera.id);
          if (!ok) failedCameras.push(camera.name || camera.id);
        }
      }
      if (failedCameras.length > 0) {
        throw new Error(`Failed assignments for ${failedCameras.slice(0, 3).join(', ')}`);
      }

      await Promise.all([fetchCameras(), fetchNodes()]);
      closeDialog();
    }, {
      loading: { title: 'Updating...', description: `Applying camera assignments for ${props.nodeName}.` },
      success: { title: 'Saved', description: 'Node camera assignments updated.' },
      error: { title: 'Failed', description: 'Could not update node camera assignments.' },
    });
  };

  return (
    <ArkDialog
      trigger={(_, setOpen) => (
        <button
          onClick={(e) => { e.stopPropagation(); prepareOpen(setOpen); }}
          class="px-3 py-1.5 rounded-lg border border-neu-750 bg-neu-800 hover:bg-neu-850 text-neu-300 text-xs font-medium transition-colors flex items-center gap-1.5"
        >
          <FiCamera class="w-3 h-3" />
          Cameras
        </button>
      )}
      title="Assign Cameras"
      description={`Add or remove cameras from node "${props.nodeName}".`}
    >
      {(setOpen) => (
        <div class="mt-4 space-y-3 max-h-[50vh] overflow-y-auto">
          <Show
            when={cameras().length > 0}
            fallback={<div class="text-sm text-neu-500">No cameras found.</div>}
          >
            <For each={cameras()}>
              {(camera) => (
                <label class="flex items-start gap-2 rounded-lg border border-neu-800 bg-neu-900/45 px-3 py-2 cursor-pointer">
                  <input
                    type="checkbox"
                    class="mt-0.5"
                    checked={selectedCameraIDs().includes(camera.id)}
                    onChange={(e) => toggleCamera(camera.id, e.currentTarget.checked)}
                  />
                  <span class="min-w-0">
                    <span class="block text-sm text-neu-100 font-medium">{camera.name}</span>
                    <span class="block text-xs text-neu-500 font-mono break-all">{camera.addr}:{camera.port}{camera.path}</span>
                  </span>
                </label>
              )}
            </For>
          </Show>
          <div class="flex justify-end pt-3">
            <button onClick={() => handleSave(() => setOpen(false))} class="btn-primary">
              Save Assignments
            </button>
          </div>
        </div>
      )}
    </ArkDialog>
  );
}

export function ManagedNodesView() {
  onMount(() => {
    fetchManagedNodes();
    fetchSystemStatus();
  });

  const handleStart = (id: string) => {
    toaster.promise(async () => {
      const ok = await startManagedNode(id);
      if (!ok) throw new Error('Failed to start node');
    }, {
      loading: { title: 'Starting...', description: 'Spawning node process (may take ~30s for Go compile).' },
      success: { title: 'Started', description: 'Node process started.' },
      error: { title: 'Failed', description: 'Could not start node.' },
    });
  };

  const handleStop = (id: string) => {
    toaster.promise(async () => {
      const ok = await stopManagedNode(id);
      if (!ok) throw new Error('Failed to stop node');
    }, {
      loading: { title: 'Stopping...', description: 'Sending stop signal.' },
      success: { title: 'Stopped', description: 'Node process stopped.' },
      error: { title: 'Failed', description: 'Could not stop node.' },
    });
  };

  const isRunning = (id: string) => !!systemStatus()[`node-${id}`]?.running;
  const isOnline = (id: string) => !!nodes().find(n => n.id === id && n.status === 'online');
  const cameraAssignedToNode = (camera: CameraInfo, nodeID: string) => {
    const nodeIDs = camera.node_ids && camera.node_ids.length > 0
      ? camera.node_ids
      : (camera.managed_node_id ? [camera.managed_node_id] : []);
    return nodeIDs.includes(nodeID);
  };

  // Count cameras assigned to each node
  const cameraCount = (id: string) => cameras().filter(c => cameraAssignedToNode(c, id)).length;

  return (
    <div class="h-full min-h-0 flex flex-col bg-neu-925/20 rounded-2xl border border-neu-800/70 overflow-hidden">
      <div class="px-5 py-4 border-b border-neu-800/70 bg-neu-900/45">
        <div class="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h3 class="text-sm font-semibold text-neu-100">Managed Nodes</h3>
            <p class="text-xs text-neu-500 mt-1">Create nodes, assign cameras, and control runtime lifecycle.</p>
          </div>
          <div class="flex items-center gap-2">
            <button
              onClick={() => { fetchManagedNodes(); fetchSystemStatus(); }}
              class="px-3 py-1.5 rounded-lg border border-neu-750 bg-neu-800 hover:bg-neu-850 text-neu-300 text-xs font-medium transition-colors flex items-center gap-1.5"
            >
              <FiRefreshCw class="w-3 h-3" />
              Refresh
            </button>
            <CreateNodeDialog />
          </div>
        </div>
      </div>
      <Show
        when={managedNodes().length > 0}
        fallback={
          <div class="flex-1 min-h-0 flex items-center justify-center text-neu-500 px-6">
            <div class="saas-empty w-full max-w-xl">
              <FiServer class="mx-auto mb-4 w-12 h-12" />
              <p>No managed nodes</p>
              <p class="text-sm mt-1">Click "Create Node" to add a node, then assign cameras to it</p>
            </div>
          </div>
        }
      >
        <div class="flex-1 min-h-0 relative overflow-x-auto overflow-y-auto">
          <table class="data-table">
            <thead>
              <tr>
                <th scope="col" class="px-6 py-3 font-medium">Name</th>
                <th scope="col" class="px-6 py-3 font-medium">Status</th>
                <th scope="col" class="px-6 py-3 font-medium">Cameras</th>
                <th scope="col" class="px-6 py-3 font-medium">Actions</th>
              </tr>
            </thead>
            <tbody>
              <For each={managedNodes()}>
                {(mn) => (
                  <tr class="transition-colors">
                    <td class="px-6 py-4 font-medium text-white">{mn.name}</td>
                    <td class="px-6 py-4">
                      <StatusDot running={isRunning(mn.id)} online={isOnline(mn.id)} />
                    </td>
                    <td class="px-6 py-4">
                      <span class="text-xs text-neu-300">{cameraCount(mn.id)} camera(s)</span>
                    </td>
                    <td class="px-6 py-4">
                      <div class="flex items-center gap-2">
                        <Show
                          when={isRunning(mn.id)}
                          fallback={
                            <button
                              onClick={() => handleStart(mn.id)}
                              class="px-3 py-1.5 rounded-lg border border-green-700 bg-green-900/30 hover:bg-green-900/50 text-green-400 text-xs font-medium transition-colors flex items-center gap-1.5"
                            >
                              <FiPlay class="w-3 h-3" />
                              Start
                            </button>
                          }
                        >
                          <button
                            onClick={() => handleStop(mn.id)}
                            class="px-3 py-1.5 rounded-lg border border-red-700 bg-red-900/30 hover:bg-red-900/50 text-red-400 text-xs font-medium transition-colors flex items-center gap-1.5"
                          >
                            <FiSquare class="w-3 h-3" />
                            Stop
                          </button>
                        </Show>
                        <NodeCameraAssignDialog nodeID={mn.id} nodeName={mn.name} />
                        <RenameNodeDialog id={mn.id} currentName={mn.name} />
                        <DeleteNodeDialog id={mn.id} name={mn.name} />
                      </div>
                    </td>
                  </tr>
                )}
              </For>
            </tbody>
          </table>
        </div>
      </Show>
    </div>
  );
}

// ── Cameras View ───────────────────────────────────────────────────────────

function CameraDeleteDialog(props: { camera: CameraInfo }) {
  const handleDelete = async () => {
    toaster.promise(async () => {
      const ok = await deleteCamera(props.camera.id);
      if (!ok) throw new Error('Failed to delete camera');
    }, {
      loading: { title: 'Deleting...', description: 'Removing camera.' },
      success: { title: 'Deleted', description: 'Camera removed successfully.' },
      error: { title: 'Failed', description: 'Could not delete camera.' },
    });
  };

  return (
    <ArkDialog
      trigger={(_, setOpen) => (
        <button
          onClick={(e) => { e.stopPropagation(); setOpen(true); }}
          class="px-3 py-1.5 rounded-lg border border-neu-750 bg-neu-800 hover:bg-neu-850 text-neu-300 text-xs font-medium transition-colors flex items-center gap-1.5"
        >
          <FiTrash2 class="w-3 h-3" />
          Delete
        </button>
      )}
      title="Delete Camera"
      description={`Are you sure you want to delete "${props.camera.name}"? This action cannot be undone.`}
    >
      <div class="flex justify-end pt-4">
        <Dialog.CloseTrigger>
          <button onClick={handleDelete} class="btn-danger">Delete Camera</button>
        </Dialog.CloseTrigger>
      </div>
    </ArkDialog>
  );
}

function CameraEditDialog(props: { camera: CameraInfo }) {
  const [name, setName] = createSignal(props.camera.name);
  const [addr, setAddr] = createSignal(props.camera.addr);
  const [port, setPort] = createSignal(props.camera.port);
  const [path, setPath] = createSignal(props.camera.path || '');
  const [authUsername, setAuthUsername] = createSignal('');
  const [authPassword, setAuthPassword] = createSignal('');
  const [selectedNodes, setSelectedNodes] = createSignal<string[]>(
    props.camera.node_ids && props.camera.node_ids.length > 0
      ? [...props.camera.node_ids]
      : (props.camera.managed_node_id ? [props.camera.managed_node_id] : [])
  );
  const toggleNode = (nodeID: string, checked: boolean) => {
    setSelectedNodes((prev) => {
      if (checked) return prev.includes(nodeID) ? prev : [...prev, nodeID];
      return prev.filter((id) => id !== nodeID);
    });
  };
  const normalizedSelectedNodes = () =>
    Array.from(new Set(selectedNodes().map((id) => id.trim()).filter(Boolean)));

  const handleSave = async () => {
    if (!name().trim() || !addr().trim() || port() <= 0) return;
    toaster.promise(async () => {
      const ok = await editCamera(props.camera.id, {
        name: name().trim(),
        addr: addr().trim(),
        port: port(),
        path: path().trim(),
        auth_username: authUsername().trim() || undefined,
        auth_password: authPassword().trim() || undefined,
        node_ids: normalizedSelectedNodes(),
      });
      if (!ok) throw new Error('Failed to update camera');
      await Promise.all([fetchCameras(), fetchNodes()]);
    }, {
      loading: { title: 'Saving...', description: 'Updating camera.' },
      success: { title: 'Saved', description: 'Camera updated successfully.' },
      error: { title: 'Failed', description: 'Could not update camera.' },
    });
  };

  return (
    <ArkDialog
      trigger={(_, setOpen) => (
        <button
          onClick={(e) => { e.stopPropagation(); setOpen(true); }}
          class="px-3 py-1.5 rounded-lg border border-neu-750 bg-neu-800 hover:bg-neu-850 text-neu-300 text-xs font-medium transition-colors flex items-center gap-1.5"
        >
          <FiEdit2 class="w-3 h-3" />
          Edit
        </button>
      )}
      title="Edit Camera"
      description="Update camera connection details"
    >
      <div class="mt-4 space-y-3">
        <div>
          <label class="text-sm font-medium text-neu-300">Name *</label>
          <input type="text" value={name()} onInput={(e) => setName(e.currentTarget.value)}
            class="px-3 py-1.5 mt-1 block w-full rounded-lg bg-neu-850 border border-neu-750 text-white focus:outline-none" />
        </div>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="text-sm font-medium text-neu-300">Address *</label>
            <input type="text" value={addr()} onInput={(e) => setAddr(e.currentTarget.value)}
              class="px-3 py-1.5 mt-1 block w-full rounded-lg bg-neu-850 border border-neu-750 text-white focus:outline-none" />
          </div>
          <div>
            <label class="text-sm font-medium text-neu-300">Port *</label>
            <input type="number" value={port()} onInput={(e) => setPort(parseInt(e.currentTarget.value) || 0)}
              class="px-3 py-1.5 mt-1 block w-full rounded-lg bg-neu-850 border border-neu-750 text-white focus:outline-none" />
          </div>
        </div>
        <div>
          <label class="text-sm font-medium text-neu-300">Path</label>
          <input type="text" value={path()} onInput={(e) => setPath(e.currentTarget.value)}
            placeholder="/stream1"
            class="px-3 py-1.5 mt-1 block w-full rounded-lg bg-neu-850 border border-neu-750 text-white focus:outline-none placeholder:text-neu-500" />
        </div>
        <div>
          <label class="text-sm font-medium text-neu-300">Assign to Nodes</label>
          <div class="mt-1 max-h-40 overflow-y-auto rounded-lg bg-neu-850 border border-neu-750 p-2 space-y-1">
            <Show when={managedNodes().length > 0} fallback={<div class="text-xs text-neu-500 px-1 py-2">No nodes available.</div>}>
              <For each={managedNodes()}>
                {(mn) => (
                  <label class="flex items-start gap-2 rounded-md px-2 py-1.5 hover:bg-neu-800/70 cursor-pointer">
                    <input
                      type="checkbox"
                      class="mt-0.5"
                      checked={selectedNodes().includes(mn.id)}
                      onChange={(e) => toggleNode(mn.id, e.currentTarget.checked)}
                    />
                    <span class="min-w-0">
                      <span class="block text-sm text-neu-200">{mn.name}</span>
                      <span class="block text-[11px] text-neu-600 font-mono break-all">{mn.id}</span>
                    </span>
                  </label>
                )}
              </For>
            </Show>
          </div>
          <div class="mt-1 text-xs text-neu-500">
            Select one or more nodes. Leave all unchecked to keep this camera unassigned.
          </div>
        </div>
        <div class="border-t border-neu-750 pt-3">
          <label class="text-xs font-medium text-neu-500 uppercase tracking-wide">Authentication (leave blank to keep existing)</label>
          <div class="grid grid-cols-2 gap-3 mt-2">
            <input type="text" value={authUsername()} onInput={(e) => setAuthUsername(e.currentTarget.value)}
              placeholder="Username"
              class="px-3 py-1.5 block w-full rounded-lg bg-neu-850 border border-neu-750 text-white focus:outline-none placeholder:text-neu-500" />
            <input type="password" value={authPassword()} onInput={(e) => setAuthPassword(e.currentTarget.value)}
              placeholder="Password"
              class="px-3 py-1.5 block w-full rounded-lg bg-neu-850 border border-neu-750 text-white focus:outline-none placeholder:text-neu-500" />
          </div>
        </div>
        <div class="flex justify-end pt-2">
          <Dialog.CloseTrigger>
            <button onClick={handleSave} disabled={!name().trim() || !addr().trim() || port() <= 0}
              class="btn-primary disabled:opacity-50">
              Save Changes
            </button>
          </Dialog.CloseTrigger>
        </div>
      </div>
    </ArkDialog>
  );
}

function AddCameraDialog() {
  const [name, setName] = createSignal('');
  const [cameraType, setCameraType] = createSignal('rtsp');
  const [addr, setAddr] = createSignal('');
  const [port, setPort] = createSignal(554);
  const [path, setPath] = createSignal('');
  const [authUsername, setAuthUsername] = createSignal('');
  const [authPassword, setAuthPassword] = createSignal('');
  const [selectedNodes, setSelectedNodes] = createSignal<string[]>([]);
  const toggleNode = (nodeID: string, checked: boolean) => {
    setSelectedNodes((prev) => {
      if (checked) return prev.includes(nodeID) ? prev : [...prev, nodeID];
      return prev.filter((id) => id !== nodeID);
    });
  };
  const normalizedSelectedNodes = () =>
    Array.from(new Set(selectedNodes().map((id) => id.trim()).filter(Boolean)));

  const resetForm = () => {
    setName(''); setCameraType('rtsp'); setAddr(''); setPort(554); setPath('');
    setAuthUsername(''); setAuthPassword(''); setSelectedNodes([]);
  };

  const handleAdd = async () => {
    if (!name().trim() || !addr().trim()) return;
    toaster.promise(async () => {
      const ok = await createCamera({
        name: name().trim(),
        type: cameraType(),
        addr: addr().trim(),
        port: port(),
        path: path().trim(),
        auth_username: authUsername().trim() || undefined,
        auth_password: authPassword().trim() || undefined,
        node_ids: normalizedSelectedNodes(),
      });
      if (!ok) throw new Error('Failed to add camera');
      resetForm();
    }, {
      loading: { title: 'Adding...', description: 'Creating camera.' },
      success: { title: 'Added', description: 'Camera added successfully.' },
      error: { title: 'Failed', description: 'Could not add camera.' },
    });
  };

  return (
    <ArkDialog
      trigger={(_, setOpen) => (
        <button onClick={() => setOpen(true)} class="btn-primary flex items-center gap-1.5">
          <FiPlus class="w-4 h-4" />
          Add Camera
        </button>
      )}
      title="Add Camera"
      description="Enter the RTSP or MJPEG camera details"
    >
      <div class="mt-4 space-y-3">
        <div>
          <label class="text-sm font-medium text-neu-300">Name *</label>
          <input
            type="text"
            value={name()}
            onInput={(e) => setName(e.currentTarget.value)}
            placeholder="e.g. Front Door Camera"
            class="px-3 py-1.5 mt-1 block w-full rounded-lg bg-neu-850 border border-neu-750 text-white focus:outline-none placeholder:text-neu-500"
          />
        </div>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="text-sm font-medium text-neu-300">Type</label>
            <select
              value={cameraType()}
              onChange={(e) => {
                setCameraType(e.currentTarget.value);
                if (e.currentTarget.value === 'rtsp') setPort(554);
                else setPort(80);
              }}
              class="px-3 py-1.5 mt-1 block w-full rounded-lg bg-neu-850 border border-neu-750 text-white focus:outline-none"
            >
              <option value="rtsp">RTSP</option>
              <option value="mjpeg">MJPEG</option>
            </select>
          </div>
          <div>
            <label class="text-sm font-medium text-neu-300">Port *</label>
            <input
              type="number"
              value={port()}
              onInput={(e) => setPort(parseInt(e.currentTarget.value) || 0)}
              class="px-3 py-1.5 mt-1 block w-full rounded-lg bg-neu-850 border border-neu-750 text-white focus:outline-none"
            />
          </div>
        </div>
        <div>
          <label class="text-sm font-medium text-neu-300">Address *</label>
          <input
            type="text"
            value={addr()}
            onInput={(e) => setAddr(e.currentTarget.value)}
            placeholder="e.g. 192.168.1.100"
            class="px-3 py-1.5 mt-1 block w-full rounded-lg bg-neu-850 border border-neu-750 text-white focus:outline-none placeholder:text-neu-500"
          />
        </div>
        <div>
          <label class="text-sm font-medium text-neu-300">Path</label>
          <input
            type="text"
            value={path()}
            onInput={(e) => setPath(e.currentTarget.value)}
            placeholder="e.g. /stream1 or /cam/realmonitor"
            class="px-3 py-1.5 mt-1 block w-full rounded-lg bg-neu-850 border border-neu-750 text-white focus:outline-none placeholder:text-neu-500"
          />
        </div>
        <div>
          <label class="text-sm font-medium text-neu-300">Assign to Nodes</label>
          <div class="mt-1 max-h-40 overflow-y-auto rounded-lg bg-neu-850 border border-neu-750 p-2 space-y-1">
            <Show when={managedNodes().length > 0} fallback={<div class="text-xs text-neu-500 px-1 py-2">No nodes available.</div>}>
              <For each={managedNodes()}>
                {(mn) => (
                  <label class="flex items-start gap-2 rounded-md px-2 py-1.5 hover:bg-neu-800/70 cursor-pointer">
                    <input
                      type="checkbox"
                      class="mt-0.5"
                      checked={selectedNodes().includes(mn.id)}
                      onChange={(e) => toggleNode(mn.id, e.currentTarget.checked)}
                    />
                    <span class="min-w-0">
                      <span class="block text-sm text-neu-200">{mn.name}</span>
                      <span class="block text-[11px] text-neu-600 font-mono break-all">{mn.id}</span>
                    </span>
                  </label>
                )}
              </For>
            </Show>
          </div>
          <div class="mt-1 text-xs text-neu-500">
            Select one or more nodes. Leave all unchecked to create a standalone camera.
          </div>
        </div>
        <div class="border-t border-neu-750 pt-3">
          <label class="text-xs font-medium text-neu-500 uppercase tracking-wide">Authentication (optional)</label>
          <div class="grid grid-cols-2 gap-3 mt-2">
            <input
              type="text"
              value={authUsername()}
              onInput={(e) => setAuthUsername(e.currentTarget.value)}
              placeholder="Username"
              class="px-3 py-1.5 block w-full rounded-lg bg-neu-850 border border-neu-750 text-white focus:outline-none placeholder:text-neu-500"
            />
            <input
              type="password"
              value={authPassword()}
              onInput={(e) => setAuthPassword(e.currentTarget.value)}
              placeholder="Password"
              class="px-3 py-1.5 block w-full rounded-lg bg-neu-850 border border-neu-750 text-white focus:outline-none placeholder:text-neu-500"
            />
          </div>
        </div>
        <div class="flex justify-end pt-4">
          <Dialog.CloseTrigger>
            <button
              onClick={handleAdd}
              disabled={!name().trim() || !addr().trim() || port() <= 0}
              class="btn-primary disabled:opacity-50"
            >
              Add Camera
            </button>
          </Dialog.CloseTrigger>
        </div>
      </div>
    </ArkDialog>
  );
}

export function CamerasView() {
  onMount(() => {
    fetchCameras();
    fetchManagedNodes();
  });

  const nodeNames = (camera: CameraInfo): string[] => {
    const nodeIDs = camera.node_ids && camera.node_ids.length > 0
      ? camera.node_ids
      : (camera.managed_node_id ? [camera.managed_node_id] : []);
    if (nodeIDs.length === 0) return [];
    return nodeIDs
      .map(id => managedNodes().find(mn => mn.id === id)?.name || id)
      .filter(Boolean);
  };

  return (
    <div class="h-full min-h-0 flex flex-col bg-neu-925/20 rounded-2xl border border-neu-800/70 overflow-hidden">
      <div class="px-5 py-4 border-b border-neu-800/70 bg-neu-900/45">
        <div class="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h3 class="text-sm font-semibold text-neu-100">Cameras / Services</h3>
            <p class="text-xs text-neu-500 mt-1">Maintain camera endpoints and map each feed to one, many, or no nodes.</p>
          </div>
          <AddCameraDialog />
        </div>
      </div>
      <Show
        when={!camerasLoading()}
        fallback={<div class="flex-1 min-h-0 flex items-center justify-center py-12 text-neu-500">Loading cameras...</div>}
      >
        <Show
          when={cameras().length > 0}
          fallback={
            <div class="flex-1 min-h-0 flex items-center justify-center text-neu-500 px-6">
              <div class="saas-empty w-full max-w-xl">
                <FiCamera class="mx-auto mb-4 w-12 h-12" />
                <p>No cameras added yet</p>
                <p class="text-sm mt-1">Click "Add Camera" to add an RTSP or MJPEG camera</p>
              </div>
            </div>
          }
        >
          <div class="flex-1 min-h-0 relative overflow-x-auto overflow-y-auto">
            <table class="data-table">
              <thead>
                <tr>
                  <th scope="col" class="px-6 py-3 font-medium">Name</th>
                  <th scope="col" class="px-6 py-3 font-medium">Type</th>
                  <th scope="col" class="px-6 py-3 font-medium">Address</th>
                  <th scope="col" class="px-6 py-3 font-medium">Nodes</th>
                  <th scope="col" class="px-6 py-3 font-medium">Added</th>
                  <th scope="col" class="px-6 py-3 font-medium">Actions</th>
                </tr>
              </thead>
              <tbody>
                <For each={cameras()}>
                  {(camera) => (
                    <tr class="transition-colors">
                      <td class="px-6 py-4 font-medium text-white">{camera.name}</td>
                      <td class="px-6 py-4">
                        <span class="bg-neu-800 text-neu-300 text-xs font-medium px-2.5 py-0.5 rounded uppercase">
                          {camera.type}
                        </span>
                      </td>
                      <td class="px-6 py-4">
                        <span class="text-xs font-mono">{camera.addr}:{camera.port}{camera.path}</span>
                      </td>
                      <td class="px-6 py-4">
                        <Show when={nodeNames(camera).length > 0} fallback={<span class="text-xs text-neu-600">—</span>}>
                          <div class="flex flex-wrap gap-1.5">
                            <For each={nodeNames(camera)}>
                              {(name) => (
                                <span class="text-[11px] px-2 py-0.5 rounded border border-violet-500/30 bg-violet-500/10 text-violet-300">
                                  {name}
                                </span>
                              )}
                            </For>
                          </div>
                        </Show>
                      </td>
                      <td class="px-6 py-4 text-xs text-neu-400">
                        {new Date(camera.created_at).toLocaleDateString()}
                      </td>
                      <td class="px-6 py-4">
                        <div class="flex items-center gap-2">
                          <CameraEditDialog camera={camera} />
                          <CameraDeleteDialog camera={camera} />
                        </div>
                      </td>
                    </tr>
                  )}
                </For>
              </tbody>
            </table>
          </div>
        </Show>
      </Show>
    </div>
  );
}

// ── Main ───────────────────────────────────────────────────────────────────

export default function HomeContent() {
  const [activeTab, setActiveTab] = createSignal('nodes');
  const [recentEvents, setRecentEvents] = createSignal<AgentEvent[]>([]);

  onMount(() => {
    fetchNodes();
    fetchManagedNodes();
    fetchSystemStatus();
    fetchRecentAgentEventsRest({ limit: 300 }).then((evs) => setRecentEvents(evs)).catch(() => {});
  });

  const kpi = () => {
    const events = recentEvents();
    let violations = 0;
    const personCount = new Map<string, number>();
    const cameraCount = new Map<string, number>();
    for (const ev of events) {
      const meta: any = ev.metadata || {};
      const data: any = ev.data || {};
      const vc = Number(meta.violation_count ?? data.violation_count ?? 0);
      if (vc > 0 || meta.has_violations === true || data.has_violations === true) violations++;
      const personName = String(meta.person_name || data.person_name || '').trim();
      if (personName) personCount.set(personName, (personCount.get(personName) || 0) + 1);
      const serviceID = String(meta.service_id || ev.service_ids?.[0] || '').trim();
      if (serviceID) cameraCount.set(serviceID, (cameraCount.get(serviceID) || 0) + 1);
    }
    const repeatOffender = [...personCount.entries()].sort((a, b) => b[1] - a[1])[0];
    const hotCamera = [...cameraCount.entries()].sort((a, b) => b[1] - a[1])[0];
    return {
      violations,
      repeatOffender: repeatOffender ? `${repeatOffender[0]} (${repeatOffender[1]})` : 'N/A',
      hotCamera: hotCamera ? `${hotCamera[0]} (${hotCamera[1]})` : 'N/A',
    };
  };

  return (
    <Show
      when={!nodesLoading()}
      fallback={
        <div class="h-full flex items-center justify-center">
          <div class="text-neu-500">Loading...</div>
        </div>
      }
    >
      <div class="h-full flex flex-col bg-neu-925/20">
        <div class="px-6 pt-4 pb-2 grid grid-cols-1 sm:grid-cols-3 gap-3">
          <div class="bg-neu-900/78 border border-neu-800/85 rounded-xl p-3 shadow-sm">
            <div class="text-[11px] text-neu-500">Recent Violations</div>
            <div class="text-lg font-semibold text-red-400">{kpi().violations}</div>
          </div>
          <div class="bg-neu-900/78 border border-neu-800/85 rounded-xl p-3 shadow-sm">
            <div class="text-[11px] text-neu-500">Repeat Offender</div>
            <div class="text-sm font-semibold text-neu-200">{kpi().repeatOffender}</div>
          </div>
          <div class="bg-neu-900/78 border border-neu-800/85 rounded-xl p-3 shadow-sm">
            <div class="text-[11px] text-neu-500">Camera Heat</div>
            <div class="text-sm font-semibold text-neu-200">{kpi().hotCamera}</div>
          </div>
        </div>
        <div class="flex-1 min-h-0">
          <ArkTabs
            items={[
              {
                value: 'nodes',
                label: 'Nodes',
                icon: <FiServer size={16} />,
                content: <ManagedNodesView />
              },
              {
                value: 'cameras',
                label: 'Cameras',
                icon: <FiCamera size={16} />,
                content: <CamerasView />
              },
            ]}
            value={activeTab()}
            onValueChange={setActiveTab}
            listClass="px-6"
          />
        </div>
      </div>
    </Show>
  );
}
