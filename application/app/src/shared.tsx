import { createSignal } from "solid-js";
import posthog from "./lib/posthog";


const RAW_BASE_URL = (import.meta.env.VITE_RELAY_API_URL || '').trim();

const isLocalAddress = (host: string) =>
  host === "localhost" || host === "127.0.0.1" || host === "[::1]" || host.endsWith(".localhost");

const resolveBaseURL = () => {
  if (!RAW_BASE_URL) {
    return window.location.origin;
  }

  try {
    const configured = new URL(RAW_BASE_URL, window.location.origin);
    const browserHost = window.location.hostname.toLowerCase();
    const configuredHost = configured.hostname.toLowerCase();

    // When app is accessed from a public hostname via tunnel, a localhost base URL
    // would otherwise point visitors to their own machine. Fall back to same-origin.
    if (isLocalAddress(configuredHost) && !isLocalAddress(browserHost)) {
      return window.location.origin;
    }
    return configured.href;
  } catch {
    // Relative paths should still resolve against the current origin.
    if (RAW_BASE_URL.startsWith('/')) {
      return new URL(RAW_BASE_URL, window.location.origin).href;
    }
  }

  return window.location.origin;
};

const BASE_URL = resolveBaseURL();
export const relay = (path: string) => new URL(path, BASE_URL).href


// JWT token management
const TOKEN_KEY = 'auth_token';

export const setToken = (token: string) => localStorage.setItem(TOKEN_KEY, token);
export const getToken = () => localStorage.getItem(TOKEN_KEY);
export const clearToken = () => localStorage.removeItem(TOKEN_KEY);

// Helper function to make authenticated API requests
// Automatically adds JWT token and relay base URL
export const relayFetch = async (path: string, options?: RequestInit): Promise<Response> => {
  const token = getToken();

  // Convert HeadersInit to Record<string, string>
  const headers: Record<string, string> = {};
  if (options?.headers) {
    if (options.headers instanceof Headers) {
      options.headers.forEach((value, key) => {
        headers[key] = value;
      });
    } else if (Array.isArray(options.headers)) {
      options.headers.forEach(([key, value]) => {
        headers[key] = value;
      });
    } else {
      Object.assign(headers, options.headers);
    }
  }

  if (token) {
    headers['Authorization'] = `Bearer ${token}`;
  }

  // Add dev impersonation header if config allows it
  const devImpersonate = configState().DEV_IMPERSONATE;
  if (devImpersonate === "true" || devImpersonate.includes("@")) {
    // If devImpersonate is "true", caller must set X-Dev-Impersonate manually
    // If devImpersonate is an email, we'll use it for auto-impersonation
    if (devImpersonate.includes("@")) {
      headers['X-Dev-Impersonate'] = devImpersonate;
    }
  }

  return fetch(relay(path), {
    ...options,
    headers,
    credentials: 'omit', // No longer need cookies
  });
};

type RelayResponseBody = {
  text: string;
  json?: any;
};

const readRelayResponseBody = async (response: Response): Promise<RelayResponseBody> => {
  const text = await response.text().catch(() => '');
  if (!text.trim()) {
    return { text };
  }
  try {
    return { text, json: JSON.parse(text) };
  } catch {
    return { text };
  }
};

const relayFailureMessage = (response: Response, body: RelayResponseBody, fallback: string): string => {
  const jsonMessage = typeof body.json?.message === 'string' ? body.json.message.trim() : '';
  if (jsonMessage) {
    return jsonMessage;
  }
  const text = body.text.trim();
  if (text) {
    return text;
  }
  return `${fallback} (HTTP ${response.status})`;
};

const relayNetworkErrorMessage = (error: unknown): string => {
  if (error instanceof Error) {
    const message = error.message.trim();
    if (message === 'Failed to fetch') {
      return 'Could not reach the relay. Check that the relay is still running.';
    }
    if (message) {
      return message;
    }
  }
  return 'Network error. Please try again.';
};


// Auth types
export interface User {
  id: number;
  email: string;
  name: string;
}

export interface AuthState {
  user: User | null;
  isAuthenticated: boolean;
  isLoading: boolean;
}

// Auth state
export const [authState, setAuthState] = createSignal<AuthState>({
  user: null,
  isAuthenticated: false,
  isLoading: true,
});

// Config state
export interface ConfigState {
  DEV_IMPERSONATE: string; // "", "true", or specific email
  SIGNUP_ENABLED: boolean;
  WEBRTC_ICE_SERVERS: RTCIceServer[];
  isLoading: boolean;
}

export const [configState, setConfigState] = createSignal<ConfigState>({
  DEV_IMPERSONATE: "",
  SIGNUP_ENABLED: false,
  WEBRTC_ICE_SERVERS: [{ urls: 'stun:stun.l.google.com:19302' }],
  isLoading: true,
});

// Fetch flags from relay
export const fetchFlags = async () => {
  try {
    const response = await relayFetch('/flags');
    if (response.ok) {
      const data = await response.json();
      setConfigState({
        DEV_IMPERSONATE: data.DEV_IMPERSONATE || "",
        SIGNUP_ENABLED: data.SIGNUP_ENABLED === true,
        WEBRTC_ICE_SERVERS: Array.isArray(data.WEBRTC_ICE_SERVERS) && data.WEBRTC_ICE_SERVERS.length > 0
          ? data.WEBRTC_ICE_SERVERS
          : [{ urls: 'stun:stun.l.google.com:19302' }],
        isLoading: false,
      });
      console.log('[fetchFlags] Config loaded:', data);
    } else {
      setConfigState({
        DEV_IMPERSONATE: "",
        SIGNUP_ENABLED: false,
        WEBRTC_ICE_SERVERS: [{ urls: 'stun:stun.l.google.com:19302' }],
        isLoading: false,
      });
    }
  } catch (error) {
    console.error('[fetchFlags] Failed to fetch flags:', error);
    setConfigState({
      DEV_IMPERSONATE: "",
      SIGNUP_ENABLED: false,
      WEBRTC_ICE_SERVERS: [{ urls: 'stun:stun.l.google.com:19302' }],
      isLoading: false,
    });
  }
};

// Login function
export const login = async (email: string, password: string): Promise<{ success: boolean; message?: string; user?: User }> => {
  try {
    const response = await relayFetch('/auth/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ email, password }),
    });

    const body = await readRelayResponseBody(response);
    const data = body.json;

    if (response.ok && data.success) {
      setToken(data.token); // Store JWT token
      setAuthState({
        user: data.user,
        isAuthenticated: true,
        isLoading: false,
      });
      posthog.identify(String(data.user.id));
      posthog.capture('user_logged_in', { email: data.user.email });
      return { success: true, user: data.user };
    }
    posthog.capture('login_failed', { email, status: response.status });
    return { success: false, message: relayFailureMessage(response, body, 'Login failed') };
  } catch (error) {
    console.error('Login error:', error);
    return { success: false, message: relayNetworkErrorMessage(error) };
  }
};

// Register function
export const register = async (email: string, password: string, name: string, username?: string): Promise<{ success: boolean; message?: string; user?: User }> => {
  try {
    const body: Record<string, string> = { email, password, name };
    if (username) body.username = username;
    const response = await relayFetch('/auth/register', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });

    const parsed = await readRelayResponseBody(response);
    const data = parsed.json;

    if (response.ok && data.success) {
      posthog.capture('user_registered', { email, name });
      return { success: true, message: 'Registration successful', user: data.user };
    }
    return { success: false, message: relayFailureMessage(response, parsed, 'Registration failed') };
  } catch (error) {
    console.error('Register error:', error);
    return { success: false, message: relayNetworkErrorMessage(error) };
  }
};

// Logout function
export const logout = async () => {
  clearToken(); // Remove JWT from localStorage
  setAuthState({
    user: null,
    isAuthenticated: false,
    isLoading: false,
  });
  // Stop live WebSocket — must happen before token is gone so no ghost events fire
  disconnectAgentEventsWebSocket();
  posthog.capture('user_logged_out');
  posthog.reset();
  try {
    await relayFetch('/auth/logout', { method: 'POST' });
  } catch (error) {
    console.error('Logout error:', error);
  }
};

// Initialize auth by checking JWT token with server
export const initAuth = async () => {
  console.log('[initAuth] Starting auth check...');
  try {
    const response = await relayFetch('/auth/me');

    console.log('[initAuth] Response status:', response.status, response.ok);

    if (response.ok) {
      const data = await response.json();
      console.log('[initAuth] Response data:', data);
      if (data.success) {
        setAuthState({
          user: data.user,
          isAuthenticated: true,
          isLoading: false,
        });
        console.log('[initAuth] User authenticated:', data.user);
      } else {
        clearToken();
        setAuthState({
          user: null,
          isAuthenticated: false,
          isLoading: false,
        });
        console.log('[initAuth] Response not successful');
      }
    } else {
      clearToken();
      setAuthState({
        user: null,
        isAuthenticated: false,
        isLoading: false,
      });
      console.log('[initAuth] Response not OK, setting unauthenticated');
    }
  } catch (error) {
    console.error('[initAuth] Auth check failed:', error);
    clearToken();
    setAuthState({
      user: null,
      isAuthenticated: false,
      isLoading: false,
    });
  }
};

export type SimpleTabType =
  | 'command'
  | 'investigate'
  | 'timeline'
  | 'performance'
  | 'control'
  | 'identity'
  | 'system';

export type Tab =
  | { type: SimpleTabType }
  | {
    type: 'view';
    nodeId: string;
    serviceId: string;
    name?: string;
  };

export const [tab, setTab] = createSignal<Tab>({ type: 'command' });

// Node services state
export interface NodeService {
  id: string;
  name: string;
  type: string;
  node_id: string;
  addr: string;
  port: number;
  path: string;
}

export interface NodeInfo {
  id: string;
  services: NodeService[];
  status: 'online' | 'offline';
  name?: string;
  lastConnectedAt?: string;
}

export const [nodes, setNodes] = createSignal<NodeInfo[]>([]);
export const [nodesLoading, setNodesLoading] = createSignal(true);

// Agent state
export interface AgentInfo {
  id: string;
  name: string;
  instruction: string;
  worker_id: string;
  service_ids: string[];
  created_at: string;
  updated_at?: string;
  kind?: 'standard' | 'custom_yolo';
  custom_model_filename?: string;
  custom_model_size_bytes?: number;
  custom_runtime_defaults?: {
    enable_vlm: boolean;
    enable_face: boolean;
  };
  custom_runtime_desired_replicas?: number;
}

export const [agents, setAgents] = createSignal<AgentInfo[]>([]);
export const [agentsLoading, setAgentsLoading] = createSignal(true);

export const fetchAgents = async () => {
  setAgentsLoading(true);
  try {
    const response = await relayFetch('/agents');

    if (response.ok) {
      const data = await response.json(); // Array of agent objects
      setAgents(data);
    } else if (response.status === 401) {
      // Unauthorized - redirect to login
      window.location.href = '/login';
    } else {
      console.error('Failed to fetch agents');
      setAgents([]);
    }
  } catch (error) {
    console.error('Error fetching agents:', error);
    setAgents([]);
  } finally {
    setAgentsLoading(false);
  }
};

export const patchAgent = async (
  agentId: string,
  patch: { name?: string; instruction?: string; service_ids?: string[] }
): Promise<boolean> => {
  try {
    const response = await relayFetch(`/agents/${encodeURIComponent(agentId)}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(patch),
    });

    if (response.status === 401) {
      // normal-login mode: force login if token expired
      window.location.href = "/login";
      return false;
    }

    if (!response.ok) {
      const text = await response.text().catch(() => "");
      console.error("[patchAgent] Failed:", response.status, text);
      return false;
    }

    return true;
  } catch (e) {
    console.error("[patchAgent] Error:", e);
    return false;
  }
};

export const createAgent = async (
  payload: { name: string; instruction: string; worker_id: string; service_ids?: string[] }
): Promise<{ ok: boolean; agent?: AgentInfo; error?: string }> => {
  try {
    const response = await relayFetch('/agents', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name: payload.name,
        instruction: payload.instruction,
        worker_id: payload.worker_id,
        service_ids: payload.service_ids ?? [],
      }),
    });

    if (!response.ok) {
      const text = await response.text().catch(() => '');
      return { ok: false, error: text || `HTTP ${response.status}` };
    }

    const body = await response.json().catch(() => ({} as { agent?: AgentInfo }));
    await fetchAgents();
    return { ok: true, agent: body.agent };
  } catch (error) {
    return {
      ok: false,
      error: error instanceof Error ? error.message : 'Network error',
    };
  }
};



// Camera grouping (Bird-eye)
export interface CameraGroup {
  id: string;
  name: string;
  service_ids: string[];
  created_at?: string;
  updated_at?: string;
}

export const [cameraGroups, setCameraGroups] = createSignal<CameraGroup[]>([]);
export const [cameraGroupsLoading, setCameraGroupsLoading] = createSignal(true);

export const fetchCameraGroups = async () => {
  setCameraGroupsLoading(true);
  try {
    const response = await relayFetch('/camera-groups');
    if (response.ok) {
      const data = await response.json();
      setCameraGroups(data);
    } else {
      setCameraGroups([]);
    }
  } catch (e) {
    console.error('Error fetching camera groups:', e);
    setCameraGroups([]);
  } finally {
    setCameraGroupsLoading(false);
  }
};

export const createCameraGroup = async (name: string) => {
  const response = await relayFetch('/camera-groups', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name })
  });
  if (response.ok) {
    await fetchCameraGroups();
    return true;
  }
  return false;
};

export const renameCameraGroup = async (groupId: string, name: string) => {
  const response = await relayFetch(`/camera-groups/${groupId}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name })
  });
  if (response.ok) {
    await fetchCameraGroups();
    return true;
  }
  return false;
};

export const deleteCameraGroup = async (groupId: string) => {
  const response = await relayFetch(`/camera-groups/${groupId}`, { method: 'DELETE' });
  if (response.ok) {
    await fetchCameraGroups();
    return true;
  }
  return false;
};

export const addCameraToGroup = async (groupId: string, serviceId: string) => {
  const response = await relayFetch(`/camera-groups/${groupId}/members`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ service_id: serviceId })
  });
  if (response.ok) {
    // Optimistic update: reflect change immediately in local state
    setCameraGroups(prev => prev.map(g =>
      g.id === groupId
        ? { ...g, service_ids: [...new Set([...(g.service_ids || []), serviceId])] }
        : g
    ));
    return true;
  }
  return false;
};

export const removeCameraFromGroup = async (groupId: string, serviceId: string) => {
  const response = await relayFetch(`/camera-groups/${groupId}/members/${serviceId}`, { method: 'DELETE' });
  if (response.ok) {
    // Optimistic update: reflect change immediately in local state
    setCameraGroups(prev => prev.map(g =>
      g.id === groupId
        ? { ...g, service_ids: (g.service_ids || []).filter(id => id !== serviceId) }
        : g
    ));
    return true;
  }
  return false;
};

/**
 * Bulk-set the members of a camera group.
 * Fires all add/remove requests in parallel, then refreshes once.
 */
export const setCameraGroupMembers = async (groupId: string, serviceIds: string[]) => {
  const group = cameraGroups().find(g => g.id === groupId);
  if (!group) return false;
  const current = new Set(group.service_ids || []);
  const target = new Set(serviceIds);
  const toAdd = serviceIds.filter(id => !current.has(id));
  const toRemove = [...current].filter(id => !target.has(id));

  // Optimistic update first
  setCameraGroups(prev => prev.map(g => g.id === groupId ? { ...g, service_ids: serviceIds } : g));

  await Promise.all([
    ...toAdd.map(id => relayFetch(`/camera-groups/${groupId}/members`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ service_id: id })
    })),
    ...toRemove.map(id => relayFetch(`/camera-groups/${groupId}/members/${id}`, { method: 'DELETE' })),
  ]);

  // Confirm with server
  await fetchCameraGroups();
  return true;
};

// Camera management state
export interface CameraInfo {
  id: string;
  name: string;
  type: string;
  addr: string;
  port: number;
  path: string;
  processing_fps?: number;
  vlm_fps?: number;
  managed_node_id?: string;
  node_ids?: string[];
  created_at: string;
}

export const [cameras, setCameras] = createSignal<CameraInfo[]>([]);
export const [camerasLoading, setCamerasLoading] = createSignal(false);

export const fetchCameras = async () => {
  setCamerasLoading(true);
  try {
    const response = await relayFetch('/cameras');
    if (response.ok) {
      const data = await response.json();
      setCameras(data);
    } else {
      setCameras([]);
    }
  } catch (e) {
    console.error('Error fetching cameras:', e);
    setCameras([]);
  } finally {
    setCamerasLoading(false);
  }
};

export const createCamera = async (data: { name: string; type: string; addr: string; port: number; path: string; auth_username?: string; auth_password?: string; managed_node_id?: string; node_ids?: string[]; processing_fps?: number; vlm_fps?: number }) => {
  const response = await relayFetch('/cameras', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(data)
  });
  if (response.ok) {
    await fetchCameras();
    await fetchNodes(); // Refresh services list since camera appears as service
    return true;
  }
  const detail = await response.text().catch(() => '');
  console.error('[shared.createCamera] failed:', response.status, detail || '(no response body)');
  return false;
};

export const editCamera = async (cameraId: string, data: { name: string; addr: string; port: number; path: string; auth_username?: string; auth_password?: string; node_ids?: string[]; processing_fps?: number; vlm_fps?: number }) => {
  const response = await relayFetch(`/cameras/${cameraId}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(data)
  });
  if (response.ok) {
    await fetchCameras();
    await fetchNodes(); // Refresh services list
    return true;
  }
  const detail = await response.text().catch(() => '');
  console.error('[shared.editCamera] failed:', response.status, detail || '(no response body)');
  return false;
};

export const deleteCamera = async (cameraId: string) => {
  const response = await relayFetch(`/cameras/${cameraId}`, { method: 'DELETE' });
  if (response.ok) {
    await fetchCameras();
    await fetchNodes(); // Refresh services list
    return true;
  }
  return false;
};

export const fetchNodes = async () => {
  setNodesLoading(true);
  try {
    const response = await relayFetch('/nodes');

    if (response.ok) {
      const data = await response.json(); // Array of {id, status, name, last_connected_at}

      // Fetch services only for online nodes
      const nodesWithServices: NodeInfo[] = await Promise.all(
        data.map(async (nodeData: { id: string; status: string; name?: string; last_connected_at?: string }) => {
          let services: NodeService[] = [];

          // Only fetch services if node is online
          if (nodeData.status === 'online') {
            try {
              const servicesResp = await relayFetch(`/nodes/${nodeData.id}/services`);
              if (servicesResp.ok) {
                services = await servicesResp.json();
              }
            } catch {
              // Ignore service fetch errors for individual nodes
            }
          }

          return {
            id: nodeData.id,
            status: nodeData.status as 'online' | 'offline',
            name: nodeData.name,
            lastConnectedAt: nodeData.last_connected_at,
            services
          };
        })
      );

      setNodes(nodesWithServices);
    } else if (response.status === 401) {
      // Unauthorized - redirect to login
      window.location.href = '/login';
    } else {
      console.error('Failed to fetch nodes');
      setNodes([]);
    }
  } catch (error) {
    console.error('Error fetching nodes:', error);
    setNodes([]);
  } finally {
    setNodesLoading(false);
  }
};

// Agent Events WebSocket
export interface AgentEvent {
  id: string;
  agent_id: string;
  agent_name: string;
  service_ids: string[];
  data: Record<string, unknown>;
  metadata?: Record<string, unknown>;
  created_at: string;
}

let agentEventsWs: WebSocket | null = null;
let agentEventsWsConnecting = false;
let agentEventsWsRegistered = false;
let agentEventsWsShouldReconnect = true;

// Messages queued before WS is registered
const pendingAgentEventsMessages: any[] = [];

const agentEventListeners = new Set<(event: AgentEvent) => void>();
const agentOverlayListeners = new Set<(event: AgentEvent) => void>();

function flushPendingAgentEventMessages() {
  if (!agentEventsWs || agentEventsWs.readyState !== WebSocket.OPEN) return;
  if (!agentEventsWsRegistered) return;
  while (pendingAgentEventsMessages.length) {
    try {
      agentEventsWs.send(JSON.stringify(pendingAgentEventsMessages.shift()));
    } catch (e) {
      console.error('[AgentEvents] Failed to flush queued message', e);
      break;
    }
  }
}

function sendAgentEventsMessageOrQueue(msg: any) {
  if (!agentEventsWs || agentEventsWs.readyState !== WebSocket.OPEN || !agentEventsWsRegistered) {
    pendingAgentEventsMessages.push(msg);
    // Ensure we have a connection attempt in-flight
    connectAgentEventsWebSocket();
    return;
  }
  agentEventsWs.send(JSON.stringify(msg));
}

export const connectAgentEventsWebSocket = () => {
  const token = getToken();
  if (!token) {
    console.error('[AgentEvents] No token available');
    return;
  }

  // If there is already a live/connecting socket, do nothing
  if (agentEventsWs && (agentEventsWs.readyState === WebSocket.OPEN || agentEventsWs.readyState === WebSocket.CONNECTING)) {
    return;
  }
  if (agentEventsWsConnecting) return;

  agentEventsWsConnecting = true;
  agentEventsWsRegistered = false;
  agentEventsWsShouldReconnect = true;

  const wsUrl = relay('/client/connect').replace(/^http/, 'ws');

  const ws = new WebSocket(wsUrl);
  agentEventsWs = ws;

  ws.onopen = () => {
    console.log('[AgentEvents] Connected, registering...');

    // Send registration message with JWT token
    ws.send(JSON.stringify({
      type: 'register',
      data: { token }
    }));
  };

  ws.onmessage = (event) => {
    try {
      const message = JSON.parse(event.data);

      switch (message.type) {
        case 'registered':
          agentEventsWsRegistered = true
          console.log('[AgentEvents] Registered as user', message.data.user_id);
          flushPendingAgentEventMessages();
          break;

        case 'error':
          console.error('[AgentEvents] Error:', message.data.message);
          ws.close();
          break;

        case 'agent_event': {
          const agentEvent = message.data as AgentEvent;
          agentEventListeners.forEach(listener => listener(agentEvent));
          break;
        }
        case 'agent_overlay_event': {
          const agentEvent = message.data as AgentEvent;
          agentOverlayListeners.forEach(listener => listener(agentEvent));
          break;
        }

        case 'res_agent_events': {
          // Handle historical events response
          const events = message.data.events as AgentEvent[];
          console.log('[AgentEvents] Received', events.length, 'historical events');
          // Emit each historical event to listeners (in reverse order to maintain chronological order)
          events.reverse().forEach(historicalEvent => {
            agentEventListeners.forEach(listener => listener(historicalEvent));
          });
          break;
        }

        case 'node_auth_request': {
          const { node_id, auth_url } = message.data as NodeAuthRequest;
          if (node_id) {
            setPendingNodeAuth(prev => {
              if (prev.some(r => r.node_id === node_id)) return prev;
              return [...prev, { node_id, auth_url }];
            });
          }
          break;
        }

        default:
          console.warn('[AgentEvents] Unknown message type:', message.type);
      }
    } catch (error) {
      console.error('[AgentEvents] Failed to parse message:', error);
    }
  };

  ws.onerror = (error) => {
    console.error('[AgentEvents] WebSocket error:', error);
  };

  ws.onclose = () => {
    agentEventsWsConnecting = false;
    agentEventsWsRegistered = false;

    // Only clear if this is still the active connection
    if (agentEventsWs === ws) {
      agentEventsWs = null;
    }

    if (!agentEventsWsShouldReconnect) {
      console.log('[AgentEvents] WebSocket closed');
      return;
    }

    console.log('[AgentEvents] WebSocket closed, reconnecting in 3s...');
    setTimeout(() => connectAgentEventsWebSocket(), 3000);
  };

  // If connection stays open, clear connecting flag once open/close happens
  ws.addEventListener('open', () => {
    agentEventsWsConnecting = false;
  });
};

export const subscribeToAgentEvents = (callback: (event: AgentEvent) => void) => {
  agentEventListeners.add(callback);
  return () => agentEventListeners.delete(callback);
};

export const subscribeToAgentOverlayEvents = (callback: (event: AgentEvent) => void) => {
  agentOverlayListeners.add(callback);
  return () => agentOverlayListeners.delete(callback);
};

export const disconnectAgentEventsWebSocket = () => {
  agentEventsWsShouldReconnect = false;
  pendingAgentEventsMessages.length = 0;
  if (agentEventsWs) {
    try {
      agentEventsWs.close();
    } catch {}
    agentEventsWs = null;
  }
  agentEventsWsConnecting = false;
  agentEventsWsRegistered = false;
  agentEventListeners.clear();
  agentOverlayListeners.clear();
};

export const requestAgentEvents = (params: { agent_id?: string; service_id?: string; limit?: number }) => {
  sendAgentEventsMessageOrQueue({
    type: 'req_agent_events',
    data: params
  });
};
// Agent Events REST (used by Search tab)
export const fetchRecentAgentEventsRest = async (params?: { limit?: number; offset?: number }): Promise<AgentEvent[]> => {
  const limit = params?.limit ?? 50;
  const offset = params?.offset ?? 0;
  const response = await relayFetch(`/events?limit=${encodeURIComponent(String(limit))}&offset=${encodeURIComponent(String(offset))}`);
  if (!response.ok) return [];
  try {
    const data = await response.json();
    return Array.isArray(data) ? (data as AgentEvent[]) : [];
  } catch {
    return [];
  }
};

export const fetchEventsForDate = async (date: string, params?: { limit?: number; offset?: number }): Promise<AgentEvent[]> => {
  const limit = params?.limit ?? 500;
  const offset = params?.offset ?? 0;
  const response = await relayFetch(
    `/events?date=${encodeURIComponent(date)}&limit=${encodeURIComponent(String(limit))}&offset=${encodeURIComponent(String(offset))}`
  );
  if (!response.ok) return [];
  try {
    const data = await response.json();
    return Array.isArray(data) ? (data as AgentEvent[]) : [];
  } catch { return []; }
};

export const fetchEventCount = async (): Promise<{ total: number; today: number }> => {
  try {
    const res = await relayFetch('/events/count');
    if (!res.ok) return { total: 0, today: 0 };
    return await res.json() as { total: number; today: number };
  } catch { return { total: 0, today: 0 }; }
};

// ── Daily Summaries ────────────────────────────────────────────────────────

export interface DailySummaryPersonSighting {
  person_id: string;
  global_person_id?: string;
  person_name: string;
  count: number;
  first_seen: string;
  last_seen: string;
  cameras?: string[];
  activity?: string;
}

export interface DailySummaryIncident {
  event_id: string;
  severity: string;
  brief: string;
  snapshot_path?: string;
  service_id?: string;
  person_name?: string;
  created_at: string;
}

export interface DailySummary {
  date: string;
  event_count: number;
  person_sightings: DailySummaryPersonSighting[];
  incidents: DailySummaryIncident[];
  violation_breakdown: Record<string, number>;
  updated_at?: string;
}

export const fetchDailySummary = async (date?: string): Promise<DailySummary | null> => {
  try {
    const q = date ? `?date=${encodeURIComponent(date)}` : '';
    const res = await relayFetch(`/daily-summary${q}`);
    if (!res.ok) return null;
    return await res.json() as DailySummary;
  } catch { return null; }
};

export const getGlobalFPS = async (): Promise<number> => {
  try {
    const res = await relayFetch('/system/frame-interval');
    if (!res.ok) return 15;
    const data = await res.json() as { fps: number };
    return data.fps || 15;
  } catch { return 15; }
};

export const setGlobalFPS = async (fps: number): Promise<{ fps: number; interval_ms: number; updated_streams: number } | null> => {
  try {
    const res = await relayFetch('/system/frame-interval', {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ fps }),
    });
    if (!res.ok) return null;
    return await res.json() as { fps: number; interval_ms: number; updated_streams: number };
  } catch { return null; }
};

export const searchAgentEventsRest = async (query: string, params?: { limit?: number; offset?: number }): Promise<AgentEvent[]> => {
  const q = (query || '').trim();
  if (!q) return [];
  const limit = params?.limit ?? 50;
  const offset = params?.offset ?? 0;
  const response = await relayFetch(
    `/search?query=${encodeURIComponent(q)}&limit=${encodeURIComponent(String(limit))}&offset=${encodeURIComponent(String(offset))}`
  );
  if (!response.ok) return [];
  try {
    const data = await response.json();
    return Array.isArray(data) ? (data as AgentEvent[]) : [];
  } catch {
    return [];
  }
};

// ── System Status ─────────────────────────────────────────────────────────

export interface ProcessInfo {
  running: boolean;
  pid?: number;
  started_at?: string;
}
export type SystemStatus = Record<string, ProcessInfo>;

export const [systemStatus, setSystemStatus] = createSignal<SystemStatus>({});

export async function fetchSystemStatus(): Promise<void> {
  try {
    const res = await relayFetch('/system/status');
    if (!res.ok) return;
    const data = await res.json() as SystemStatus;
    setSystemStatus(data);
  } catch {}
}

export async function startComponent(name: string): Promise<boolean> {
  try {
    const res = await relayFetch(`/system/start/${encodeURIComponent(name)}`, { method: 'POST' });
    await fetchSystemStatus();
    return res.ok;
  } catch {
    return false;
  }
}

export async function stopComponent(name: string): Promise<boolean> {
  try {
    const res = await relayFetch(`/system/stop/${encodeURIComponent(name)}`, { method: 'POST' });
    await fetchSystemStatus();
    return res.ok;
  } catch {
    return false;
  }
}

export interface StartAgentResult {
  started: string[];
  error?: string;
}

export async function startAgent(workerId: string): Promise<StartAgentResult> {
  const res = await relayFetch('/system/start/agent', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ worker_id: workerId }),
  });
  const body = await res.text();
  await Promise.all([fetchSystemStatus(), fetchSystemWorkers()]);
  if (!res.ok) throw new Error(body.trim() || `HTTP ${res.status}`);
  try {
    return JSON.parse(body) as StartAgentResult;
  } catch {
    return { started: [] };
  }
}

export interface WorkerReplicaInfo {
  name: string;
  running: boolean;
  pid?: number;
}

export interface WorkerPoolInfo {
  desired: number;
  workers: WorkerReplicaInfo[];
  adapters: WorkerReplicaInfo[];
  reason?: string;
  connected_workers?: number;
  total_dropped?: number;
  queue_fill_pct?: number; // 0-100
}

export type SystemWorkersResponse = Record<'ppe' | 'objdet' | 'vlm' | 'face', WorkerPoolInfo>;

export interface LLMWorkerStatus {
  running: boolean;
  pid?: number;
  model_id: string;
  port: number;
  ready: boolean;
  queue_depth?: number;
  last_error?: string;
  latency_ms?: number;
}

export interface SystemRuntimeMetrics {
  event_persist: {
    success: number;
    failure: number;
    dropped: number;
  };
  snapshot_upload: {
    success: number;
    failure: number;
  };
  identity_resolve: {
    resolve_count: number;
    avg_latency_ms: number;
  };
}

let supportsLLMStatusEndpoint = true;
let supportsRuntimeSettingsEndpoint = true;

function applyLegacyLLMStatusFromSystemStatus() {
  const st = systemStatus();
  const proc = st['llm_query'] || st['llm'];
  if (!proc) return;
  setLLMWorkerStatus((prev) => ({
    ...prev,
    running: !!proc.running,
    ready: !!proc.running,
    pid: proc.pid,
  }));
}

export const [systemWorkers, setSystemWorkers] = createSignal<SystemWorkersResponse>({
  ppe: { desired: 0, workers: [], adapters: [] },
  objdet: { desired: 0, workers: [], adapters: [] },
  vlm: { desired: 0, workers: [], adapters: [] },
  face: { desired: 0, workers: [], adapters: [] },
});
export const [llmWorkerStatus, setLLMWorkerStatus] = createSignal<LLMWorkerStatus>({
  running: false,
  model_id: 'google/gemma-3-1b-it',
  port: 5003,
  ready: false,
});
export const [systemRuntimeMetrics, setSystemRuntimeMetrics] = createSignal<SystemRuntimeMetrics>({
  event_persist: { success: 0, failure: 0, dropped: 0 },
  snapshot_upload: { success: 0, failure: 0 },
  identity_resolve: { resolve_count: 0, avg_latency_ms: 0 },
});

export async function fetchSystemWorkers(): Promise<void> {
  try {
    const res = await relayFetch('/system/workers');
    if (!res.ok) return;
    const body = await res.json() as {
      workers?: SystemWorkersResponse;
      autoscaling?: boolean;
      autoscaleMax?: number;
      llm?: LLMWorkerStatus;
      metrics?: SystemRuntimeMetrics;
    } | SystemWorkersResponse;
    const data = (body as any).workers ? (body as any).workers as SystemWorkersResponse : body as SystemWorkersResponse;
    if (data) setSystemWorkers(data);
    if (typeof (body as any).autoscaling === 'boolean') setAutoScalingEnabled((body as any).autoscaling);
    if (typeof (body as any).autoscaleMax === 'number') setAutoScalingMax((body as any).autoscaleMax);
    if ((body as any).llm && typeof (body as any).llm === 'object') {
      setLLMWorkerStatus((body as any).llm as LLMWorkerStatus);
    }
    if ((body as any).metrics && typeof (body as any).metrics === 'object') {
      setSystemRuntimeMetrics((body as any).metrics as SystemRuntimeMetrics);
    }
  } catch {}
}

export async function fetchLLMWorkerStatus(): Promise<void> {
  if (!supportsLLMStatusEndpoint) {
    applyLegacyLLMStatusFromSystemStatus();
    return;
  }
  try {
    const res = await relayFetch('/system/llm/status');
    if (res.status === 404 || res.status === 405 || res.status === 501) {
      supportsLLMStatusEndpoint = false;
      applyLegacyLLMStatusFromSystemStatus();
      return;
    }
    if (!res.ok) {
      applyLegacyLLMStatusFromSystemStatus();
      return;
    }
    const body = await res.json() as LLMWorkerStatus;
    if (body && typeof body === 'object') setLLMWorkerStatus(body);
  } catch {
    applyLegacyLLMStatusFromSystemStatus();
  }
}

export async function startLLMWorker(modelId?: string): Promise<boolean> {
  try {
    let res = await relayFetch('/system/llm/start', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ model_id: modelId || undefined }),
    });
    if (res.status === 404 || res.status === 405 || res.status === 501) {
      supportsLLMStatusEndpoint = false;
      // Compatibility fallback for older relay versions.
      res = await relayFetch('/system/start/llm_query', { method: 'POST' });
      if ((res.status === 404 || res.status === 405 || res.status === 501) && !res.ok) {
        res = await relayFetch('/system/start/llm', { method: 'POST' });
      }
    }
    if (!res.ok) {
      const text = await res.text().catch(() => '');
      console.error('Failed to start LLM worker:', res.status, text);
    }
    await Promise.all([fetchSystemStatus(), fetchSystemWorkers()]);
    await fetchLLMWorkerStatus();
    return res.ok;
  } catch (err) {
    console.error('Failed to start LLM worker:', err);
    return false;
  }
}

export async function stopLLMWorker(): Promise<boolean> {
  try {
    let res = await relayFetch('/system/llm/stop', { method: 'POST' });
    if (res.status === 404 || res.status === 405 || res.status === 501) {
      supportsLLMStatusEndpoint = false;
      // Compatibility fallback for older relay versions.
      res = await relayFetch('/system/stop/llm_query', { method: 'POST' });
      if ((res.status === 404 || res.status === 405 || res.status === 501) && !res.ok) {
        res = await relayFetch('/system/stop/llm', { method: 'POST' });
      }
    }
    if (!res.ok) {
      const text = await res.text().catch(() => '');
      console.error('Failed to stop LLM worker:', res.status, text);
    }
    await Promise.all([fetchSystemStatus(), fetchSystemWorkers()]);
    await fetchLLMWorkerStatus();
    return res.ok;
  } catch (err) {
    console.error('Failed to stop LLM worker:', err);
    return false;
  }
}

export interface RuntimeSettings {
  ppe_require_person_association: boolean;
  snapshot_cooldown_seconds: number;
  person_reid_match_threshold: number;
  llm_model_id: string;
}

export const [runtimeSettings, setRuntimeSettings] = createSignal<RuntimeSettings>({
  ppe_require_person_association: true,
  snapshot_cooldown_seconds: 30,
  person_reid_match_threshold: 0.84,
  llm_model_id: 'google/gemma-3-1b-it',
});

export async function fetchRuntimeSettings(): Promise<RuntimeSettings | null> {
  if (!supportsRuntimeSettingsEndpoint) return null;
  try {
    const res = await relayFetch('/system/runtime-settings');
    if (res.status === 404 || res.status === 405 || res.status === 501) {
      supportsRuntimeSettingsEndpoint = false;
      return null;
    }
    if (!res.ok) return null;
    const body = await res.json() as RuntimeSettings;
    setRuntimeSettings(body);
    return body;
  } catch {
    return null;
  }
}

export async function updateRuntimeSettings(patch: Partial<RuntimeSettings> & { restart_llm_if_running?: boolean }): Promise<RuntimeSettings | null> {
  if (!supportsRuntimeSettingsEndpoint) return null;
  try {
    const res = await relayFetch('/system/runtime-settings', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(patch),
    });
    if (res.status === 404 || res.status === 405 || res.status === 501) {
      supportsRuntimeSettingsEndpoint = false;
      return null;
    }
    if (!res.ok) return null;
    const body = await res.json() as RuntimeSettings;
    setRuntimeSettings(body);
    return body;
  } catch {
    return null;
  }
}

export async function scaleSystemWorkers(type: 'ppe' | 'objdet' | 'vlm' | 'face', replicas: number): Promise<{ ok: boolean; error?: string }> {
  try {
    const res = await relayFetch('/system/scale', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ type, replicas }),
    });
    await Promise.all([fetchSystemStatus(), fetchSystemWorkers()]);
    if (res.ok) return { ok: true };
    const error = await res.text().catch(() => '');
    return { ok: false, error: error || `HTTP ${res.status}` };
  } catch {
    return { ok: false, error: 'Network error while scaling workers.' };
  }
}

export const [autoScalingEnabled, setAutoScalingEnabled] = createSignal(false);
export const [autoScalingMax, setAutoScalingMax] = createSignal(6);

export async function setSystemAutoScaling(enabled: boolean, maxReplicas?: number): Promise<boolean> {
  try {
    const res = await relayFetch('/system/autoscale', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ enabled, max_replicas: maxReplicas }),
    });
    if (!res.ok) return false;
    const body = await res.json() as { enabled?: boolean; max_replicas?: number };
    if (typeof body.enabled === 'boolean') setAutoScalingEnabled(body.enabled);
    if (typeof body.max_replicas === 'number') setAutoScalingMax(body.max_replicas);
    await fetchSystemWorkers();
    return true;
  } catch {
    return false;
  }
}

export async function syncPredefinedAgents(): Promise<AgentInfo[]> {
  const res = await relayFetch('/agents/predefined/sync', { method: 'POST' });
  if (!res.ok) throw new Error(await res.text());
  const body = await res.json() as { agents?: AgentInfo[] };
  const list = Array.isArray(body.agents) ? body.agents : [];
  setAgents(list);
  return list;
}

export async function createCustomYoloAgent(formData: FormData): Promise<{ ok: boolean; agent?: AgentInfo; error?: string }> {
  try {
    const res = await relayFetch('/agents/custom-yolo', {
      method: 'POST',
      body: formData,
    });
    if (!res.ok) {
      const text = await res.text().catch(() => '');
      return { ok: false, error: text || `HTTP ${res.status}` };
    }
    const body = await res.json() as { agent?: AgentInfo };
    await fetchAgents();
    return { ok: true, agent: body.agent };
  } catch (e) {
    return { ok: false, error: e instanceof Error ? e.message : 'Network error' };
  }
}

export async function startCustomAgentRuntime(agentId: string, replicas: number, enableVLM: boolean, enableFace: boolean): Promise<{ ok: boolean; error?: string }> {
  try {
    const res = await relayFetch('/system/custom/start', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        agent_id: agentId,
        replicas,
        enable_vlm: enableVLM,
        enable_face: enableFace,
      }),
    });
    await Promise.all([fetchSystemStatus(), fetchSystemWorkers(), fetchCustomWorkersStatus(), fetchAgents()]);
    if (res.ok) return { ok: true };
    const error = await res.text().catch(() => '');
    return { ok: false, error: error || `HTTP ${res.status}` };
  } catch (error) {
    return { ok: false, error: relayNetworkErrorMessage(error) };
  }
}

export async function scaleCustomAgentRuntime(agentId: string, replicas: number): Promise<{ ok: boolean; error?: string }> {
  try {
    const res = await relayFetch('/system/custom/scale', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ agent_id: agentId, replicas }),
    });
    await Promise.all([fetchSystemStatus(), fetchCustomWorkersStatus(), fetchAgents()]);
    if (res.ok) return { ok: true };
    const error = await res.text().catch(() => '');
    return { ok: false, error: error || `HTTP ${res.status}` };
  } catch (error) {
    return { ok: false, error: relayNetworkErrorMessage(error) };
  }
}

export async function stopCustomAgentRuntime(agentId: string): Promise<{ ok: boolean; error?: string }> {
  try {
    const res = await relayFetch('/system/custom/stop', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ agent_id: agentId }),
    });
    await Promise.all([fetchSystemStatus(), fetchCustomWorkersStatus(), fetchAgents()]);
    if (res.ok) return { ok: true };
    const error = await res.text().catch(() => '');
    return { ok: false, error: error || `HTTP ${res.status}` };
  } catch (error) {
    return { ok: false, error: relayNetworkErrorMessage(error) };
  }
}

export interface CustomWorkerReplicaStatus {
  name: string;
  running: boolean;
  pid?: number;
}

export interface CustomWorkerAgentStatus {
  agent_id: string;
  agent_name: string;
  worker_id: string;
  model_filename?: string;
  desired: number;
  workers: CustomWorkerReplicaStatus[];
  adapters: CustomWorkerReplicaStatus[];
  enable_vlm: boolean;
  enable_face: boolean;
  connected_workers: number;
  total_dropped: number;
  queue_fill_pct: number;
}

export interface CustomWorkerAggregateStatus {
  total_desired: number;
  running_workers: number;
  running_adapters: number;
  connected_workers: number;
  total_dropped: number;
  max_queue_fill_pct: number;
}

export interface CustomWorkersStatusResponse {
  agents: CustomWorkerAgentStatus[];
  aggregate: CustomWorkerAggregateStatus;
}

export const [customWorkersStatus, setCustomWorkersStatus] = createSignal<CustomWorkersStatusResponse>({
  agents: [],
  aggregate: {
    total_desired: 0,
    running_workers: 0,
    running_adapters: 0,
    connected_workers: 0,
    total_dropped: 0,
    max_queue_fill_pct: 0,
  },
});

export async function fetchCustomWorkersStatus(): Promise<void> {
  try {
    const res = await relayFetch('/system/custom/workers');
    if (!res.ok) return;
    const body = await res.json() as CustomWorkersStatusResponse;
    if (body && Array.isArray(body.agents) && body.aggregate) {
      setCustomWorkersStatus(body);
    }
  } catch {}
}

// ── Managed Nodes ─────────────────────────────────────────────────────────

export interface ManagedNodeInfo {
  id: string;
  name: string;
  created_at: string;
  running: boolean;
  online: boolean;
}

export const [managedNodes, setManagedNodes] = createSignal<ManagedNodeInfo[]>([]);

export async function fetchManagedNodes(): Promise<void> {
  try {
    const res = await relayFetch('/managed-nodes');
    if (!res.ok) return;
    const data = await res.json() as ManagedNodeInfo[];
    setManagedNodes(Array.isArray(data) ? data : []);
  } catch {}
}

export async function createManagedNode(name: string): Promise<ManagedNodeInfo | null> {
  try {
    const res = await relayFetch('/managed-nodes', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name }),
    });
    if (!res.ok) return null;
    const node = await res.json() as ManagedNodeInfo;
    await fetchManagedNodes();
    return node;
  } catch {
    return null;
  }
}

export async function renameManagedNode(id: string, name: string): Promise<boolean> {
  try {
    const res = await relayFetch(`/managed-nodes/${encodeURIComponent(id)}/name`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name }),
    });
    await fetchManagedNodes();
    return res.ok;
  } catch {
    return false;
  }
}

export async function deleteManagedNode(id: string): Promise<boolean> {
  try {
    const res = await relayFetch(`/managed-nodes/${encodeURIComponent(id)}`, { method: 'DELETE' });
    await fetchManagedNodes();
    return res.ok;
  } catch {
    return false;
  }
}

export async function startManagedNode(id: string): Promise<boolean> {
  try {
    const res = await relayFetch(`/managed-nodes/${encodeURIComponent(id)}/start`, { method: 'POST' });
    await fetchSystemStatus();
    await fetchManagedNodes();
    await fetchNodes();
    return res.ok;
  } catch {
    return false;
  }
}

export async function stopManagedNode(id: string): Promise<boolean> {
  try {
    const res = await relayFetch(`/managed-nodes/${encodeURIComponent(id)}/stop`, { method: 'POST' });
    await fetchSystemStatus();
    await fetchManagedNodes();
    await fetchNodes();
    return res.ok;
  } catch {
    return false;
  }
}

export async function assignCameraToNode(nodeId: string, cameraId: string): Promise<boolean> {
  try {
    const res = await relayFetch(
      `/managed-nodes/${encodeURIComponent(nodeId)}/cameras/${encodeURIComponent(cameraId)}`,
      { method: 'POST' }
    );
    if (!res.ok) {
      const detail = await res.text().catch(() => '');
      console.error('[shared.assignCameraToNode] failed:', res.status, detail || '(no response body)');
    }
    return res.ok;
  } catch {
    return false;
  }
}

export async function unassignCameraFromNode(nodeId: string, cameraId: string): Promise<boolean> {
  try {
    const res = await relayFetch(
      `/managed-nodes/${encodeURIComponent(nodeId)}/cameras/${encodeURIComponent(cameraId)}`,
      { method: 'DELETE' }
    );
    if (!res.ok) {
      const detail = await res.text().catch(() => '');
      console.error('[shared.unassignCameraFromNode] failed:', res.status, detail || '(no response body)');
    }
    return res.ok;
  } catch {
    return false;
  }
}

// ── Node authorization requests ──────────────────────────────────────────────

export interface NodeAuthRequest {
  node_id: string;
  auth_url: string;
}

export const [pendingNodeAuth, setPendingNodeAuth] = createSignal<NodeAuthRequest[]>([]);

export function dismissNodeAuth(nodeId: string) {
  setPendingNodeAuth(prev => prev.filter(r => r.node_id !== nodeId));
}

export async function authorizeNode(nodeId: string): Promise<boolean> {
  try {
    const res = await relayFetch('/api/authorize', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ node_id: nodeId }),
    });
    if (res.ok) {
      dismissNodeAuth(nodeId);
    }
    return res.ok;
  } catch {
    return false;
  }
}

// ── Person / face-recognition registry ───────────────────────────────────────

export interface PersonInfo {
  id: string;
  name: string;
  has_face: boolean;
  profile_count: number;
  enrollment_ready?: boolean;
  enrollment_status_reason?: string;
  face_updated_at?: string;
  created_at: string;
}

export const [persons, setPersons] = createSignal<PersonInfo[]>([]);

export async function fetchPersons(): Promise<void> {
  try {
    const res = await relayFetch('/persons');
    if (!res.ok) return;
    const list = await res.json() as PersonInfo[];
    setPersons(Array.isArray(list) ? list : []);
  } catch {}
}

export async function createPerson(name: string): Promise<PersonInfo | null> {
  try {
    const res = await relayFetch('/persons', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name }),
    });
    if (!res.ok) return null;
    const p = await res.json() as PersonInfo;
    setPersons((prev) => [p, ...prev.filter((it) => it.id !== p.id)]);
    void fetchPersons();
    return p;
  } catch { return null; }
}

export async function deletePerson(id: string): Promise<boolean> {
  try {
    const res = await relayFetch(`/persons/${id}`, { method: 'DELETE' });
    if (res.ok) await fetchPersons();
    return res.ok;
  } catch { return false; }
}

export async function renamePerson(id: string, name: string): Promise<boolean> {
  try {
    const res = await relayFetch(`/persons/${id}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name }),
    });
    if (res.ok) await fetchPersons();
    return res.ok;
  } catch { return false; }
}

export async function uploadPersonFace(id: string, file: File): Promise<boolean> {
  const result = await uploadPersonFaceProfile(id, file);
  return result.ok;
}

export interface PersonFaceProfileInfo {
  id: string;
  person_id: string;
  created_at?: string;
  updated_at?: string;
  is_legacy?: boolean;
}

export interface PersonFaceUploadResult {
  ok: boolean;
  accepted: boolean;
  status: number;
  reason?: string;
  quality_metrics?: Record<string, unknown>;
  profile?: PersonFaceProfileInfo;
}

export async function fetchPersonFaceProfiles(personId: string): Promise<PersonFaceProfileInfo[]> {
  try {
    const res = await relayFetch(`/persons/${personId}/faces`);
    if (!res.ok) return [];
    const list = await res.json() as PersonFaceProfileInfo[];
    return Array.isArray(list) ? list : [];
  } catch { return []; }
}

export async function uploadPersonFaceProfile(personId: string, file: File): Promise<PersonFaceUploadResult> {
  try {
    const form = new FormData();
    form.append('image', file);
    const res = await relayFetch(`/persons/${personId}/faces`, { method: 'POST', body: form });
    let payload: any = null;
    try {
      payload = await res.json();
    } catch {
      payload = null;
    }

    if (res.ok) {
      await fetchPersons();
      return {
        ok: true,
        accepted: payload?.accepted !== false,
        status: res.status,
        reason: payload?.reason,
        quality_metrics: payload?.quality_metrics,
        profile: payload?.profile,
      };
    }

    return {
      ok: false,
      accepted: payload?.accepted === true,
      status: res.status,
      reason: payload?.reason || payload?.error || payload?.message || 'Upload failed',
      quality_metrics: payload?.quality_metrics,
      profile: payload?.profile,
    };
  } catch {
    return {
      ok: false,
      accepted: false,
      status: 0,
      reason: 'Network error',
      quality_metrics: {},
    };
  }
}

export async function deletePersonFaceProfile(personId: string, profileId: string): Promise<boolean> {
  try {
    const res = await relayFetch(`/persons/${personId}/faces/${encodeURIComponent(profileId)}`, {
      method: 'DELETE',
    });
    if (res.ok) await fetchPersons();
    return res.ok;
  } catch { return false; }
}

export interface PersonSighting {
  id: string;
  agent_id: string;
  data: Record<string, unknown>;
  metadata?: Record<string, unknown>;
  created_at: string;
}

export interface PersonMoment {
  id: string;
  global_person_id: string;
  person_id?: string;
  person_name?: string;
  identity_source?: string;
  identity_confidence?: number;
  service_id?: string;
  camera_name?: string;
  event_id?: string;
  track_id?: number;
  bbox_json?: string;
  snapshot_path?: string;
  activity_brief?: string;
  event_kind?: string;
  severity?: string;
  metadata_json?: string;
  dedupe_key?: string;
  review_status?: string;
  operator_note?: string;
  is_false_match?: boolean;
  reviewed_at?: string;
  created_at: string;
}

export interface TimelineIdentity {
  global_person_id: string;
  person_id?: string;
  person_name?: string;
  identity_source?: string;
  identity_confidence?: number;
  last_service_id?: string;
  last_camera_name?: string;
  first_seen_at: string;
  last_seen_at: string;
  moment_count: number;
  snapshot_path?: string;
}

export interface TimelineRouteNode {
  service_id: string;
  camera_name?: string;
  zone?: string;
  x: number;
  y: number;
  first_seen_at: string;
  last_seen_at: string;
  count: number;
  inferred?: boolean;
}

export interface TimelineJourneySegment {
  from_moment_id: string;
  to_moment_id: string;
  from_service_id: string;
  to_service_id: string;
  from_at: string;
  to_at: string;
  gap_seconds: number;
  dwell_seconds: number;
  confidence: number;
  inferred: boolean;
  reason: string;
}

export interface TimelineJourneyStats {
  first_seen_at?: string;
  last_seen_at?: string;
  moment_count: number;
  camera_count: number;
  dwell_seconds: number;
  average_confidence: number;
  gaps_over_120_sec: number;
  frequent_routes: Record<string, number>;
  camera_dwell_seconds: Record<string, number>;
}

export interface TimelineNearbyActivity {
  anchor_moment_id: string;
  window_minutes: number;
  count: number;
  moments: PersonMoment[];
}

export interface TimelineProximityEdge {
  global_person_id: string;
  person_name?: string;
  service_id?: string;
  camera_name?: string;
  overlap_count: number;
  window_seconds: number;
  last_seen_at?: string;
}

export interface TimelinePredictedCamera {
  service_id: string;
  camera_name?: string;
  probability: number;
  reason: string;
}

export interface TimelineAppearanceMatch {
  global_person_id: string;
  person_id?: string;
  person_name?: string;
  match_kind: string;
  score: number;
  last_service_id?: string;
  last_camera_name?: string;
  last_seen_at?: string;
  moment_count: number;
  snapshot_path?: string;
}

export interface TimelineGhostStep {
  from_service_id: string;
  to_service_id: string;
  start_at: string;
  end_at: string;
  reason: string;
  inferred: boolean;
}

export interface TimelineJourney {
  identity: TimelineIdentity;
  moments: PersonMoment[];
  route: TimelineRouteNode[];
  segments: TimelineJourneySegment[];
  stats: TimelineJourneyStats;
  nearby_activity: TimelineNearbyActivity[];
  proximity: TimelineProximityEdge[];
  predicted_next: TimelinePredictedCamera[];
  ghost_trail: TimelineGhostStep[];
}

export interface TimelineCase {
  id: string;
  title: string;
  status: string;
  owner?: string;
  notes?: string;
  reviewed: boolean;
  item_count: number;
  created_at: string;
  updated_at: string;
}

export interface TimelineCaseItem {
  id: string;
  case_id: string;
  moment_id: string;
  note?: string;
  created_at: string;
}

export interface TimelineSnapshotHash {
  moment_id: string;
  snapshot_path: string;
  sha256?: string;
  error?: string;
}

export interface TimelineExportManifest {
  case: TimelineCase;
  generated_at: string;
  moments: PersonMoment[];
  csv_rows: Record<string, string>[];
  snapshot_hashes: TimelineSnapshotHash[];
  notes?: string;
}

export interface TimelineTopologyNode {
  service_id: string;
  label?: string;
  zone?: string;
  x: number;
  y: number;
  adjacent_service_ids: string[];
  updated_at?: string;
}

export type TimelineMomentOpts = {
  date?: string;
  startAt?: string;
  endAt?: string;
  cameraId?: string;
  cameraGroupId?: string;
  globalPersonId?: string;
  personId?: string;
  anchorMomentId?: string;
  known?: 'all' | 'known' | 'unknown';
  eventKind?: string;
  severity?: string;
  reviewStatus?: string;
  query?: string;
  limit?: number;
};

const timelineParams = (opts?: TimelineMomentOpts) => {
  const params = new URLSearchParams();
  if (opts?.date) params.set('date', opts.date);
  if (opts?.startAt) params.set('start_at', opts.startAt);
  if (opts?.endAt) params.set('end_at', opts.endAt);
  if (opts?.cameraId) params.set('camera_id', opts.cameraId);
  if (opts?.cameraGroupId) params.set('camera_group_id', opts.cameraGroupId);
  if (opts?.globalPersonId) params.set('global_person_id', opts.globalPersonId);
  if (opts?.personId) params.set('person_id', opts.personId);
  if (opts?.anchorMomentId) params.set('anchor_moment_id', opts.anchorMomentId);
  if (opts?.known) params.set('known', opts.known);
  if (opts?.eventKind) params.set('event_kind', opts.eventKind);
  if (opts?.severity) params.set('severity', opts.severity);
  if (opts?.reviewStatus) params.set('review_status', opts.reviewStatus);
  if (opts?.query) params.set('q', opts.query);
  if (opts?.limit) params.set('limit', String(opts.limit));
  return params;
};

export async function fetchPersonSightings(personId: string, limit = 50): Promise<PersonSighting[]> {
  try {
    const res = await relayFetch(`/persons/${personId}/sightings?limit=${limit}`);
    if (!res.ok) return [];
    const list = await res.json() as PersonSighting[];
    return Array.isArray(list) ? list : [];
  } catch { return []; }
}

export async function fetchPersonMoments(personId: string, date?: string, limit = 100): Promise<PersonMoment[]> {
  try {
    const params = new URLSearchParams();
    params.set('limit', String(limit));
    if (date) params.set('date', date);
    const res = await relayFetch(`/persons/${personId}/moments?${params.toString()}`);
    if (!res.ok) return [];
    const list = await res.json() as PersonMoment[];
    return Array.isArray(list) ? list : [];
  } catch {
    return [];
  }
}

export async function fetchTimelineIdentities(opts?: TimelineMomentOpts): Promise<TimelineIdentity[]> {
  try {
    const params = timelineParams(opts);
    const qs = params.toString();
    const res = await relayFetch(`/timeline/identities${qs ? `?${qs}` : ''}`);
    if (!res.ok) return [];
    const list = await res.json() as TimelineIdentity[];
    return Array.isArray(list) ? list : [];
  } catch {
    return [];
  }
}

export async function fetchTimelineIdentityMoments(globalPersonId: string, opts?: TimelineMomentOpts): Promise<PersonMoment[]> {
  try {
    const id = encodeURIComponent(globalPersonId);
    const params = timelineParams(opts);
    const qs = params.toString();
    const res = await relayFetch(`/timeline/identities/${id}/moments${qs ? `?${qs}` : ''}`);
    if (!res.ok) return [];
    const list = await res.json() as PersonMoment[];
    return Array.isArray(list) ? list : [];
  } catch {
    return [];
  }
}

export async function fetchTimelineMoments(opts?: TimelineMomentOpts): Promise<PersonMoment[]> {
  try {
    const params = timelineParams(opts);
    const qs = params.toString();
    const res = await relayFetch(`/timeline/moments${qs ? `?${qs}` : ''}`);
    if (!res.ok) return [];
    const list = await res.json() as PersonMoment[];
    return Array.isArray(list) ? list : [];
  } catch {
    return [];
  }
}

export async function fetchTimelineJourney(globalPersonId: string, opts?: TimelineMomentOpts): Promise<TimelineJourney | null> {
  try {
    const params = timelineParams({ ...opts, globalPersonId });
    const qs = params.toString();
    const res = await relayFetch(`/timeline/journey${qs ? `?${qs}` : ''}`);
    if (!res.ok) return null;
    return await res.json() as TimelineJourney;
  } catch {
    return null;
  }
}

export async function backfillTimelineSightings(limit = 50000): Promise<{ processed: number } | null> {
  try {
    const params = new URLSearchParams();
    params.set('limit', String(limit));
    const res = await relayFetch(`/timeline/backfill?${params.toString()}`, { method: 'POST' });
    if (!res.ok) return null;
    return await res.json() as { processed: number };
  } catch {
    return null;
  }
}

export async function fetchTimelineAppearanceSearch(globalPersonId: string, opts?: { kind?: 'any' | 'body' | 'face'; limit?: number }): Promise<TimelineAppearanceMatch[]> {
  try {
    const params = new URLSearchParams();
    params.set('global_person_id', globalPersonId);
    if (opts?.kind) params.set('kind', opts.kind);
    if (opts?.limit) params.set('limit', String(opts.limit));
    const res = await relayFetch(`/timeline/appearance-search?${params.toString()}`);
    if (!res.ok) return [];
    const list = await res.json() as TimelineAppearanceMatch[];
    return Array.isArray(list) ? list : [];
  } catch {
    return [];
  }
}

export async function assignTimelineIdentity(globalPersonId: string, personId: string): Promise<{ global_person_id: string } | null> {
  try {
    const res = await relayFetch(`/timeline/identities/${encodeURIComponent(globalPersonId)}/assign`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ person_id: personId }),
    });
    if (!res.ok) return null;
    return await res.json();
  } catch {
    return null;
  }
}

export async function mergeTimelineIdentities(sourceId: string, targetId: string): Promise<boolean> {
  try {
    const res = await relayFetch(`/timeline/identities/${encodeURIComponent(sourceId)}/merge`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ target_id: targetId }),
    });
    return res.ok;
  } catch {
    return false;
  }
}

export async function splitTimelineMoments(sourceId: string, momentIds: string[]): Promise<{ global_person_id: string } | null> {
  try {
    const res = await relayFetch(`/timeline/identities/${encodeURIComponent(sourceId)}/split`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ moment_ids: momentIds }),
    });
    if (!res.ok) return null;
    return await res.json();
  } catch {
    return null;
  }
}

export async function reviewTimelineMoment(momentId: string, payload: { status?: string; note?: string; is_false_match?: boolean }): Promise<boolean> {
  try {
    const res = await relayFetch(`/timeline/moments/${encodeURIComponent(momentId)}/review`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });
    return res.ok;
  } catch {
    return false;
  }
}

export async function auditTimelineReveal(momentId: string): Promise<boolean> {
  try {
    const res = await relayFetch(`/timeline/moments/${encodeURIComponent(momentId)}/reveal`, { method: 'POST' });
    return res.ok;
  } catch {
    return false;
  }
}

export async function fetchTimelineCases(): Promise<TimelineCase[]> {
  try {
    const res = await relayFetch('/timeline/cases');
    if (!res.ok) return [];
    const list = await res.json() as TimelineCase[];
    return Array.isArray(list) ? list : [];
  } catch {
    return [];
  }
}

export async function createTimelineCase(payload: { title: string; owner?: string; notes?: string }): Promise<TimelineCase | null> {
  try {
    const res = await relayFetch('/timeline/cases', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });
    if (!res.ok) return null;
    return await res.json() as TimelineCase;
  } catch {
    return null;
  }
}

export async function updateTimelineCase(caseId: string, payload: Partial<Pick<TimelineCase, 'title' | 'status' | 'owner' | 'notes' | 'reviewed'>>): Promise<TimelineCase | null> {
  try {
    const res = await relayFetch(`/timeline/cases/${encodeURIComponent(caseId)}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });
    if (!res.ok) return null;
    return await res.json() as TimelineCase;
  } catch {
    return null;
  }
}

export async function addTimelineCaseItems(caseId: string, momentIds: string[], note?: string): Promise<TimelineCaseItem[]> {
  try {
    const res = await relayFetch(`/timeline/cases/${encodeURIComponent(caseId)}/items`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ moment_ids: momentIds, note: note || '' }),
    });
    if (!res.ok) return [];
    const list = await res.json() as TimelineCaseItem[];
    return Array.isArray(list) ? list : [];
  } catch {
    return [];
  }
}

export async function exportTimelineCase(caseId: string): Promise<TimelineExportManifest | null> {
  try {
    const res = await relayFetch(`/timeline/cases/${encodeURIComponent(caseId)}/export`, { method: 'POST' });
    if (!res.ok) return null;
    return await res.json() as TimelineExportManifest;
  } catch {
    return null;
  }
}

export async function createTimelineEvidenceLock(payload: { title?: string; case_id?: string; moment_ids: string[]; expires_at?: string }): Promise<boolean> {
  try {
    const res = await relayFetch('/timeline/evidence-locks', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });
    return res.ok;
  } catch {
    return false;
  }
}

export async function fetchTimelineTopology(): Promise<TimelineTopologyNode[]> {
  try {
    const res = await relayFetch('/timeline/topology');
    if (!res.ok) return [];
    const list = await res.json() as TimelineTopologyNode[];
    return Array.isArray(list) ? list : [];
  } catch {
    return [];
  }
}

export async function updateTimelineTopology(nodes: TimelineTopologyNode[]): Promise<TimelineTopologyNode[]> {
  try {
    const res = await relayFetch('/timeline/topology', {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ nodes }),
    });
    if (!res.ok) return [];
    const list = await res.json() as TimelineTopologyNode[];
    return Array.isArray(list) ? list : [];
  } catch {
    return [];
  }
}

// ── AI Natural Language Query ──────────────────────────────────────────────

export interface AIQueryEvent {
  id: string;
  brief: string;
  severity: string;
  snapshot_path?: string;
  service_id?: string;
  person_name?: string;
  global_person_id?: string;
  created_at: string;
}

export interface AIQueryTimelineEvidence {
  id: string;
  global_person_id: string;
  person_id?: string;
  person_name?: string;
  identity_source?: string;
  identity_confidence?: number;
  service_id?: string;
  camera_name?: string;
  event_id?: string;
  track_id?: number;
  snapshot_path?: string;
  activity_brief?: string;
  event_kind?: string;
  severity?: string;
  created_at: string;
}

export interface AIQueryResponse {
  answer: string;
  confidence: 'high' | 'medium' | 'low' | string;
  chat_id?: string;
  intent?: string;
  relevant_events: AIQueryEvent[];
  timeline_evidence?: AIQueryTimelineEvidence[];
  sources?: AIQuerySource[];
  trace_id?: string;
}

export interface AIQuerySource {
  id: string;
  title: string;
  source_uri: string;
  heading?: string;
  section_path?: string;
  version?: string;
  environment?: string;
  language?: string;
  score: number;
  snippet: string;
}

export interface AIChatMessage {
  role: 'user' | 'assistant';
  content: string;
}

export interface QueryChat {
  id: string;
  title: string;
  created_at: string;
  updated_at: string;
  messages?: AIChatMessage[];
}

export interface KBStats {
  documents: number;
  chunks: number;
  embedded_chunks: number;
  embedding_enabled?: boolean;
  embedding_endpoint?: string;
  embedding_model?: string;
  stored_embedding_model?: string;
  embedding_dim?: number;
  embedding_health?: 'ok' | 'unavailable' | 'partial' | 'disabled' | string;
  last_embedding_error?: string;
}

export interface KBReindexResult {
  docs_dir: string;
  indexed: Array<{
    path: string;
    document_id?: string;
    chunks?: number;
    embedded?: number;
    skipped?: boolean;
  }>;
}

export async function fetchKBStats(): Promise<KBStats | null> {
  try {
    const res = await relayFetch('/kb/stats');
    if (!res.ok) return null;
    return await res.json() as KBStats;
  } catch {
    return null;
  }
}

export async function reindexKnowledgeBase(): Promise<KBReindexResult> {
  const res = await relayFetch('/kb/reindex-docs', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ embed: true }),
  });
  if (!res.ok) {
    const text = await res.text().catch(() => '');
    throw new Error(text.trim() || `Reindex failed (HTTP ${res.status})`);
  }
  return await res.json() as KBReindexResult;
}

export async function fetchQueryChats(): Promise<QueryChat[]> {
  try {
    const res = await relayFetch('/query/chats');
    if (!res.ok) return [];
    const list = await res.json() as QueryChat[];
    return Array.isArray(list) ? list : [];
  } catch {
    return [];
  }
}

export async function fetchQueryChat(chatId: string): Promise<QueryChat | null> {
  try {
    const res = await relayFetch(`/query/chats/${encodeURIComponent(chatId)}`);
    if (!res.ok) return null;
    return await res.json() as QueryChat;
  } catch {
    return null;
  }
}

export async function deleteQueryChat(chatId: string): Promise<boolean> {
  try {
    const res = await relayFetch(`/query/chats/${encodeURIComponent(chatId)}`, { method: 'DELETE' });
    return res.ok;
  } catch {
    return false;
  }
}

/**
 * POST /query — natural language security query answered by the local Ask AI worker.
 * @param question  e.g. "Did someone steal anything today?"
 * @param date      "today" | "yesterday" | "YYYY-MM-DD" (default: today)
 * @param chatHistory optional recent chat turns for follow-up questions
 */
export async function queryAI(question: string, date?: string, chatHistory?: AIChatMessage[], chatId?: string): Promise<AIQueryResponse> {
  const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone || "";
  const controller = new AbortController();
  const timeoutMs = 45000;
  const timeoutId = setTimeout(() => controller.abort(), timeoutMs);
  try {
    const res = await relayFetch('/query', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        question,
        date: date ?? 'today',
        timezone,
        chat_id: chatId || undefined,
        chat_history: chatHistory ?? [],
        include_timeline: true,
      }),
      signal: controller.signal,
    });
    if (!res.ok) {
      const text = await res.text().catch(() => '');
      let errJson: { error?: string; code?: string; suggestion?: string } | null = null;
      try {
        errJson = JSON.parse(text) as { error?: string; code?: string; suggestion?: string };
      } catch {
        errJson = null;
      }
      if (errJson?.code) {
        throw new Error(`${errJson.code}: ${errJson.error || 'query failed'}${errJson.suggestion ? ` (${errJson.suggestion})` : ''}`);
      }
      throw new Error(`HTTP ${res.status}: ${text.trim().substring(0, 200) || 'query failed'}`);
    }
    return await res.json() as AIQueryResponse;
  } catch (err: any) {
    if (err?.name === 'AbortError') {
      throw new Error('Ask AI timed out. Please retry or reduce query scope.');
    }
    throw err;
  } finally {
    clearTimeout(timeoutId);
  }
}

// Auto-poll system status every 4 seconds
if (typeof window !== 'undefined') {
  const isMarketingHost = () => {
    const host = window.location.hostname.toLowerCase();
    return host === 'vyracam.in' || host === 'www.vyracam.in';
  };

  const shouldPollSystemAPIs = () => !isMarketingHost() && !!getToken();

  const pollSystemAPIs = () => {
    if (!shouldPollSystemAPIs()) return;
    fetchSystemStatus();
    fetchSystemWorkers();
    fetchCustomWorkersStatus();
    fetchLLMWorkerStatus();
    fetchManagedNodes();
  };

  setInterval(pollSystemAPIs, 4000);
  setInterval(() => {
    if (!shouldPollSystemAPIs()) return;
    fetchRuntimeSettings();
  }, 15000);

  pollSystemAPIs();
}
