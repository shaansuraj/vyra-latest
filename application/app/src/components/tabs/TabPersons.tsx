import { Show, For, createMemo, createSignal, onMount, type Component } from 'solid-js';
import { FiUser, FiPlus, FiTrash2, FiUpload, FiEdit2, FiCheck, FiX, FiClock, FiChevronDown, FiChevronUp, FiCamera, FiActivity } from 'solid-icons/fi';
import {
  persons, fetchPersons, createPerson, deletePerson, renamePerson,
  fetchPersonFaceProfiles, uploadPersonFaceProfile, deletePersonFaceProfile,
  fetchPersonSightings, fetchPersonMoments, fetchTimelineIdentities, fetchTimelineIdentityMoments,
  relay, getToken, type PersonInfo, type PersonSighting, type PersonFaceProfileInfo, type PersonMoment, type TimelineIdentity,
} from '../../shared';
import { toaster } from '../../ark/ArkToast';

const buildSnapshotUrl = (path: string) => {
  const clean = String(path || '').trim();
  if (!clean) return '';
  let base = '';
  if (/^https?:\/\//i.test(clean)) {
    base = clean;
  } else {
    const marker = '/storage/snapshots/';
    const markerPos = clean.indexOf(marker);
    if (markerPos >= 0) base = relay(clean.slice(markerPos));
    else if (clean.startsWith('/')) base = relay(clean);
    else base = relay(`/storage/snapshots/${clean.replace(/^\/+/, '')}`);
  }
  const token = getToken();
  if (!token) return base;
  const sep = base.includes('?') ? '&' : '?';
  return `${base}${sep}token=${encodeURIComponent(token)}`;
};

const todayISO = () => new Date().toISOString().slice(0, 10);

const unknownLabel = (globalPersonId: string) => {
  const id = String(globalPersonId || '').trim();
  const match = id.match(/unknown[:/#\s-]*(\d+)/i);
  if (match?.[1]) return `Unknown #${match[1]}`;
  return id || 'Unknown person';
};

// ── Sightings panel ───────────────────────────────────────────────────────────

const SightingRow: Component<{ s: PersonSighting }> = (props) => {
  const meta = () => props.s.metadata ?? {};
  const serviceId = () => (meta().service_id as string) ?? '';
  const snapshotPath = () => (meta().snapshot_path as string) ?? (props.s.data?.snapshot_path as string) ?? '';
  const matchScore = () => {
    const v = meta().match_score ?? props.s.data?.match_score;
    return typeof v === 'number' ? `${Math.round(v * 100)}%` : null;
  };
  const ts = () => new Date(props.s.created_at).toLocaleString();

  return (
    <div class="flex items-center gap-3 py-2 border-b border-neu-800/60 last:border-0">
      <div class="flex-shrink-0 w-12 h-12 rounded-lg overflow-hidden bg-neu-900 border border-neu-800">
        <Show when={snapshotPath()} fallback={<FiCamera size={20} class="text-neu-700 m-auto mt-2.5" />}>
          <img
            src={buildSnapshotUrl(snapshotPath())}
            alt="snapshot"
            class="w-full h-full object-cover"
          />
        </Show>
      </div>

      <div class="flex-1 min-w-0">
        <div class="flex items-center gap-2 text-[11px]">
          <FiClock size={10} class="text-neu-600 flex-shrink-0" />
          <span class="text-neu-400">{ts()}</span>
          <Show when={matchScore()}>
            <span class="ml-auto px-1.5 py-0.5 rounded bg-green-900/30 border border-green-700/30 text-green-400 font-mono">{matchScore()}</span>
          </Show>
        </div>
        <Show when={serviceId()}>
          <div class="text-[10px] text-neu-600 truncate mt-0.5">
            <FiCamera size={9} class="inline mr-1" />{serviceId()}
          </div>
        </Show>
      </div>
    </div>
  );
};

const MomentRow: Component<{ m: PersonMoment }> = (props) => {
  const snapshotPath = () => props.m.snapshot_path || '';
  const ts = () => new Date(props.m.created_at).toLocaleString();
  const camera = () => props.m.camera_name || props.m.service_id || '';
  const identity = () => props.m.person_name || unknownLabel(props.m.global_person_id);
  return (
    <div class="flex items-center gap-3 py-2 border-b border-neu-800/60 last:border-0">
      <div class="flex-shrink-0 w-12 h-12 rounded-lg overflow-hidden bg-neu-900 border border-neu-800">
        <Show when={snapshotPath()} fallback={<FiCamera size={20} class="text-neu-700 m-auto mt-2.5" />}>
          <img
            src={buildSnapshotUrl(snapshotPath())}
            alt="moment snapshot"
            class="w-full h-full object-cover"
          />
        </Show>
      </div>
      <div class="flex-1 min-w-0">
        <div class="text-[11px] text-neu-300 truncate">{props.m.activity_brief || props.m.event_kind || 'Activity detected'}</div>
        <div class="flex items-center gap-2 text-[10px] text-neu-500 mt-0.5">
          <FiClock size={9} />
          <span>{ts()}</span>
          <Show when={camera()}>
            <span class="truncate">· {camera()}</span>
          </Show>
        </div>
      </div>
      <div class="text-[10px] px-1.5 py-0.5 rounded border border-neu-700 text-neu-500 max-w-[120px] truncate">
        {identity()}
      </div>
    </div>
  );
};

const UnknownTimelinePanel: Component = () => {
  const [date, setDate] = createSignal(todayISO());
  const [identities, setIdentities] = createSignal<TimelineIdentity[]>([]);
  const [loading, setLoading] = createSignal(false);
  const [expanded, setExpanded] = createSignal('');
  const [momentsByID, setMomentsByID] = createSignal<Record<string, PersonMoment[]>>({});
  const [loadingMoments, setLoadingMoments] = createSignal('');

  const loadIdentities = async () => {
    setLoading(true);
    const list = await fetchTimelineIdentities({ date: date(), known: 'unknown', limit: 30 });
    setIdentities(list);
    setLoading(false);
  };

  onMount(loadIdentities);

  const toggleIdentity = async (identity: TimelineIdentity) => {
    const id = identity.global_person_id;
    if (expanded() === id) {
      setExpanded('');
      return;
    }
    setExpanded(id);
    if (!momentsByID()[id]) {
      setLoadingMoments(id);
      const moments = await fetchTimelineIdentityMoments(id, { date: date(), limit: 50 });
      setMomentsByID((prev) => ({ ...prev, [id]: moments }));
      setLoadingMoments('');
    }
  };

  return (
    <section class="rounded-2xl border border-neu-800/85 bg-neu-900/72 p-4 shadow-sm">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h3 class="text-sm font-semibold text-neu-100 flex items-center gap-2">
            <FiActivity size={14} class="text-cyan-300" />
            Unknown Activity Timeline
          </h3>
          <p class="text-[11px] text-neu-600 mt-0.5">Inspect unregistered people as timeline identities before merging them into known people.</p>
        </div>
        <div class="flex items-center gap-2">
          <input
            type="date"
            class="text-[11px] bg-neu-950 border border-neu-700 rounded px-2 py-1 text-neu-300"
            value={date()}
            onInput={(e) => setDate(e.currentTarget.value)}
          />
          <button
            class="text-[11px] px-2 py-1 rounded bg-neu-800 hover:bg-neu-700 border border-neu-700 text-neu-300 disabled:opacity-50"
            onClick={loadIdentities}
            disabled={loading()}
          >
            {loading() ? 'Loading...' : 'Refresh'}
          </button>
        </div>
      </div>

      <div class="mt-3">
        <Show when={!loading()} fallback={<p class="text-xs text-neu-600 py-2">Loading unknown identities...</p>}>
          <Show when={identities().length > 0} fallback={<p class="text-xs text-neu-700 py-2">No unknown timeline identities for this date.</p>}>
            <div class="grid grid-cols-1 xl:grid-cols-2 gap-2">
              <For each={identities()}>
                {(identity) => {
                  const open = () => expanded() === identity.global_person_id;
                  const snapshot = () => identity.snapshot_path ? buildSnapshotUrl(identity.snapshot_path) : '';
                  return (
                    <div class="rounded-xl border border-neu-800/80 bg-neu-950/45">
                      <button
                        class="w-full p-3 text-left flex items-center gap-3 hover:bg-neu-900/55 transition-colors"
                        onClick={() => toggleIdentity(identity)}
                      >
                        <div class="w-12 h-12 rounded-lg overflow-hidden border border-neu-800 bg-neu-900 flex-shrink-0">
                          <Show when={snapshot()} fallback={<FiUser size={20} class="text-neu-700 m-auto mt-2.5" />}>
                            <img src={snapshot()} alt={unknownLabel(identity.global_person_id)} class="w-full h-full object-cover" />
                          </Show>
                        </div>
                        <div class="flex-1 min-w-0">
                          <div class="flex items-center gap-2">
                            <span class="text-sm font-semibold text-neu-200 truncate">{unknownLabel(identity.global_person_id)}</span>
                            <span class="text-[10px] px-1.5 py-0.5 rounded bg-blue-500/12 border border-blue-500/25 text-blue-300">
                              {identity.moment_count} moments
                            </span>
                          </div>
                          <div class="text-[10px] text-neu-600 mt-0.5 truncate">
                            {identity.last_camera_name || identity.last_service_id || 'camera unknown'} · last seen {new Date(identity.last_seen_at).toLocaleString()}
                          </div>
                        </div>
                        {open() ? <FiChevronUp size={14} class="text-neu-500" /> : <FiChevronDown size={14} class="text-neu-500" />}
                      </button>
                      <Show when={open()}>
                        <div class="border-t border-neu-800/70 px-3 py-2 max-h-56 overflow-y-auto">
                          <Show when={loadingMoments() !== identity.global_person_id} fallback={<p class="text-xs text-neu-600 py-2">Loading moments...</p>}>
                            <For each={momentsByID()[identity.global_person_id] || []}>
                              {(moment) => <MomentRow m={moment} />}
                            </For>
                          </Show>
                        </div>
                      </Show>
                    </div>
                  );
                }}
              </For>
            </div>
          </Show>
        </Show>
      </div>
    </section>
  );
};

// ── Person card ───────────────────────────────────────────────────────────────

const PersonCard: Component<{ person: PersonInfo }> = (props) => {
  const [editing, setEditing] = createSignal(false);
  const [editName, setEditName] = createSignal('');
  const [uploading, setUploading] = createSignal(false);
  const [deletingProfileId, setDeletingProfileId] = createSignal<string | null>(null);
  const [profiles, setProfiles] = createSignal<PersonFaceProfileInfo[]>([]);
  const [loadingProfiles, setLoadingProfiles] = createSignal(true);
  const [showSightings, setShowSightings] = createSignal(false);
  const [sightings, setSightings] = createSignal<PersonSighting[]>([]);
  const [loadingSightings, setLoadingSightings] = createSignal(false);
  const [showMoments, setShowMoments] = createSignal(false);
  const [moments, setMoments] = createSignal<PersonMoment[]>([]);
  const [loadingMoments, setLoadingMoments] = createSignal(false);
  const [momentsDate, setMomentsDate] = createSignal(new Date().toISOString().slice(0, 10));

  const loadProfiles = async () => {
    setLoadingProfiles(true);
    const list = await fetchPersonFaceProfiles(props.person.id);
    setProfiles(Array.isArray(list) ? list : []);
    setLoadingProfiles(false);
  };

  onMount(loadProfiles);

  const totalProfiles = createMemo(() => {
    const loaded = profiles().length;
    if (loaded > 0) return loaded;
    if ((props.person.profile_count ?? 0) > 0) return props.person.profile_count;
    return props.person.has_face ? 1 : 0;
  });
  const enrollmentReady = createMemo(() => props.person.enrollment_ready === true);
  const enrollmentReason = createMemo(
    () => props.person.enrollment_status_reason || (enrollmentReady() ? 'Ready for recognition.' : 'Enrollment not ready.')
  );

  const primaryFaceUrl = createMemo(() => {
    if (loadingProfiles()) {
      // Avoid issuing a provisional legacy /face request that gets canceled once
      // profile metadata arrives and we switch to /faces/{profileId}.
      return '';
    }

    const first = profiles()[0];
    if (first) {
      return relay(`/persons/${props.person.id}/faces/${encodeURIComponent(first.id)}`);
    }
    if (props.person.has_face) {
      return relay(`/persons/${props.person.id}/face`);
    }
    return '';
  });

  const startEdit = () => {
    setEditName(props.person.name);
    setEditing(true);
  };

  const saveEdit = async () => {
    const name = editName().trim();
    if (!name) return;
    const ok = await renamePerson(props.person.id, name);
    if (ok) toaster.success({ title: 'Renamed', description: `Person renamed to "${name}".` });
    else toaster.error({ title: 'Failed', description: 'Could not rename person.' });
    setEditing(false);
  };

  const handleDelete = async () => {
    if (!window.confirm(`Delete "${props.person.name}" from the registry?`)) return;
    const ok = await deletePerson(props.person.id);
    if (ok) toaster.success({ title: 'Deleted', description: `${props.person.name} removed.` });
    else toaster.error({ title: 'Failed', description: 'Could not delete person.' });
  };

  const handleFaceUpload = async (e: Event) => {
    const input = e.currentTarget as HTMLInputElement;
    const file = input.files?.[0];
    if (!file) return;
    setUploading(true);
    const result = await uploadPersonFaceProfile(props.person.id, file);
    setUploading(false);
    if (result.ok) {
      toaster.success({ title: 'Profile uploaded', description: `Added new face profile for ${props.person.name}.` });
      await loadProfiles();
    } else if (result.reason) {
      toaster.error({ title: 'Profile rejected', description: result.reason });
    } else {
      toaster.error({ title: 'Upload failed', description: 'Could not save face profile.' });
    }
    input.value = '';
  };

  const handleDeleteProfile = async (profile: PersonFaceProfileInfo) => {
    const label = profile.is_legacy ? 'legacy face profile' : 'face profile';
    if (!window.confirm(`Delete this ${label} for "${props.person.name}"?`)) return;

    setDeletingProfileId(profile.id);
    const ok = await deletePersonFaceProfile(props.person.id, profile.id);
    setDeletingProfileId(null);

    if (ok) {
      toaster.success({ title: 'Profile deleted', description: `${props.person.name} profile removed.` });
      await loadProfiles();
    } else {
      toaster.error({ title: 'Failed', description: 'Could not delete face profile.' });
    }
  };

  const toggleSightings = async () => {
    const next = !showSightings();
    setShowSightings(next);
    if (next && sightings().length === 0) {
      setLoadingSightings(true);
      const list = await fetchPersonSightings(props.person.id, 20);
      setSightings(list);
      setLoadingSightings(false);
    }
  };

  const loadMoments = async () => {
    setLoadingMoments(true);
    const list = await fetchPersonMoments(props.person.id, momentsDate(), 100);
    setMoments(Array.isArray(list) ? list : []);
    setLoadingMoments(false);
  };

  const toggleMoments = async () => {
    const next = !showMoments();
    setShowMoments(next);
    if (next && moments().length === 0) {
      await loadMoments();
    }
  };

  return (
    <div class="bg-neu-900/78 border border-neu-800/85 rounded-2xl overflow-hidden shadow-sm hover:border-neu-700/85 transition-colors">
      <div class="p-4 flex gap-4">
        <div class="flex-shrink-0 w-16 h-16 rounded-xl overflow-hidden bg-neu-900 border border-neu-800 flex items-center justify-center">
          <Show when={primaryFaceUrl()} fallback={<FiUser size={28} class="text-neu-600" />}>
            <img src={primaryFaceUrl()} alt={props.person.name} class="w-full h-full object-cover" />
          </Show>
        </div>

        <div class="flex-1 min-w-0">
          <Show
            when={editing()}
            fallback={
              <div class="flex items-center gap-2 mb-1">
                <span class="text-sm font-semibold text-white truncate">{props.person.name}</span>
                <span
                  class={`px-1.5 py-0.5 rounded text-[10px] font-semibold border ${
                    enrollmentReady()
                      ? 'bg-green-900/25 text-green-300 border-green-700/40'
                      : 'bg-amber-900/20 text-amber-300 border-amber-700/40'
                  }`}
                >
                  {enrollmentReady() ? 'Ready' : 'Not Ready'}
                </span>
                <button onClick={startEdit} class="text-neu-500 hover:text-neu-200 transition-colors" title="Rename">
                  <FiEdit2 size={13} />
                </button>
              </div>
            }
          >
            <div class="flex items-center gap-1.5 mb-1">
              <input
                class="flex-1 text-sm bg-neu-900 border border-violet-600/60 rounded px-2 py-0.5 text-white outline-none"
                value={editName()}
                onInput={(e) => setEditName(e.currentTarget.value)}
                onKeyDown={(e) => { if (e.key === 'Enter') saveEdit(); if (e.key === 'Escape') setEditing(false); }}
                autofocus
              />
              <button onClick={saveEdit} class="text-green-400 hover:text-green-300"><FiCheck size={14} /></button>
              <button onClick={() => setEditing(false)} class="text-neu-500 hover:text-neu-300"><FiX size={14} /></button>
            </div>
          </Show>

          <div class="text-[11px] text-neu-600 mb-2">
            <Show when={totalProfiles() > 0} fallback={<span class="text-amber-500">No face profiles yet — upload one or more profile photos.</span>}>
              <span class="text-green-500">{totalProfiles()} profile{totalProfiles() === 1 ? '' : 's'} registered</span>
              <Show when={props.person.face_updated_at}>
                {' · '}updated {new Date(props.person.face_updated_at!).toLocaleDateString()}
              </Show>
            </Show>
            <div class={`mt-1 ${enrollmentReady() ? 'text-green-400' : 'text-amber-400'}`}>
              {enrollmentReason()}
            </div>
          </div>

          <div class="space-y-2">
            <Show when={loadingProfiles()}>
              <p class="text-[11px] text-neu-600">Loading profiles...</p>
            </Show>

            <Show when={!loadingProfiles() && profiles().length > 0}>
              <div class="flex items-center gap-2 overflow-x-auto pb-1">
                <For each={profiles()}>
                  {(profile) => (
                    <div class="relative w-14 h-14 rounded-lg overflow-hidden border border-neu-700 bg-neu-900 flex-shrink-0">
                      <img
                        src={relay(`/persons/${props.person.id}/faces/${encodeURIComponent(profile.id)}`)}
                        alt={`${props.person.name} profile`}
                        class="w-full h-full object-cover"
                      />
                      <Show when={profile.is_legacy}>
                        <span class="absolute left-1 top-1 text-[9px] px-1 py-0.5 rounded bg-neu-950/80 border border-neu-700 text-neu-300">
                          Legacy
                        </span>
                      </Show>
                      <button
                        class="absolute right-1 top-1 w-5 h-5 rounded-full bg-red-900/80 hover:bg-red-800 text-red-100 flex items-center justify-center disabled:opacity-60"
                        onClick={() => handleDeleteProfile(profile)}
                        disabled={deletingProfileId() === profile.id}
                        title="Delete profile"
                      >
                        <FiX size={11} />
                      </button>
                    </div>
                  )}
                </For>
              </div>
            </Show>
          </div>

          <div class="flex items-center gap-2 flex-wrap mt-2">
            <label class={`flex items-center gap-1.5 px-2.5 py-1 rounded-lg text-[11px] font-medium cursor-pointer
              ${uploading()
                ? 'bg-neu-800 text-neu-500 cursor-not-allowed'
                : 'bg-violet-700/30 hover:bg-violet-700/50 border border-violet-700/40 text-violet-200'}`}>
              <FiUpload size={11} />
              {uploading() ? 'Uploading...' : 'Add profile'}
              <input type="file" accept="image/*" class="hidden" onChange={handleFaceUpload} disabled={uploading()} />
            </label>

            <button
              class="flex items-center gap-1.5 px-2.5 py-1 rounded-lg text-[11px] font-medium
                bg-neu-800 hover:bg-neu-700 border border-neu-700 text-neu-300 transition-colors"
              onClick={toggleSightings}
            >
              <FiClock size={11} />
              Sightings
              {showSightings() ? <FiChevronUp size={11} /> : <FiChevronDown size={11} />}
            </button>

            <button
              class="flex items-center gap-1.5 px-2.5 py-1 rounded-lg text-[11px] font-medium
                bg-neu-800 hover:bg-neu-700 border border-neu-700 text-neu-300 transition-colors"
              onClick={toggleMoments}
            >
              <FiActivity size={11} />
              Moments
              {showMoments() ? <FiChevronUp size={11} /> : <FiChevronDown size={11} />}
            </button>

            <button
              class="flex items-center gap-1.5 px-2.5 py-1 rounded-lg text-[11px] font-medium
                bg-red-900/20 hover:bg-red-900/40 border border-red-800/30 text-red-400 transition-colors ml-auto"
              onClick={handleDelete}
            >
              <FiTrash2 size={11} /> Delete
            </button>
          </div>
          <p class="text-[10px] text-neu-600 mt-1.5">
            Upload clear single-face photos: front + left/right angles, good lighting, and close framing.
          </p>
        </div>
      </div>

      <Show when={showSightings()}>
        <div class="border-t border-neu-800 bg-neu-900/50 px-4 py-3">
          <div class="text-[11px] font-semibold text-neu-500 uppercase tracking-wider mb-2">Recent Sightings</div>
          <Show when={loadingSightings()}>
            <p class="text-xs text-neu-600 py-2">Loading...</p>
          </Show>
          <Show when={!loadingSightings() && sightings().length === 0}>
            <p class="text-xs text-neu-700 py-2">No sightings recorded yet. Start Person Detection on the assigned camera to begin tracking.</p>
          </Show>
          <Show when={!loadingSightings() && sightings().length > 0}>
            <div class="max-h-48 overflow-y-auto">
              <For each={sightings()}>
                {(s) => <SightingRow s={s} />}
              </For>
            </div>
            <button
              class="mt-2 text-[11px] text-neu-600 hover:text-neu-400 transition-colors"
              onClick={async () => {
                setLoadingSightings(true);
                const list = await fetchPersonSightings(props.person.id, 50);
                setSightings(list);
                setLoadingSightings(false);
              }}
            >
              Load more
            </button>
          </Show>
        </div>
      </Show>

      <Show when={showMoments()}>
        <div class="border-t border-neu-800 bg-neu-900/40 px-4 py-3">
          <div class="flex items-center justify-between gap-3 mb-2">
            <div class="text-[11px] font-semibold text-neu-500 uppercase tracking-wider">Cross-Camera Moments</div>
            <div class="flex items-center gap-2">
              <input
                type="date"
                class="text-[11px] bg-neu-950 border border-neu-700 rounded px-2 py-1 text-neu-300"
                value={momentsDate()}
                onInput={(e) => setMomentsDate(e.currentTarget.value)}
              />
              <button
                class="text-[11px] px-2 py-1 rounded bg-neu-800 hover:bg-neu-700 border border-neu-700 text-neu-300"
                onClick={loadMoments}
              >
                Refresh
              </button>
            </div>
          </div>
          <Show when={loadingMoments()}>
            <p class="text-xs text-neu-600 py-2">Loading moments...</p>
          </Show>
          <Show when={!loadingMoments() && moments().length === 0}>
            <p class="text-xs text-neu-700 py-2">No moments for this date.</p>
          </Show>
          <Show when={!loadingMoments() && moments().length > 0}>
            <div class="max-h-56 overflow-y-auto">
              <For each={moments()}>
                {(m) => <MomentRow m={m} />}
              </For>
            </div>
          </Show>
        </div>
      </Show>
    </div>
  );
};

// ── Main tab ──────────────────────────────────────────────────────────────────

export default function TabPersons() {
  const [newName, setNewName] = createSignal('');
  const [adding, setAdding] = createSignal(false);
  const [creating, setCreating] = createSignal(false);

  onMount(() => fetchPersons());

  const handleCreate = async () => {
    const name = newName().trim();
    if (!name) return;
    setCreating(true);
    const p = await createPerson(name);
    setCreating(false);
    if (p) {
      setNewName('');
      setAdding(false);
      toaster.success({ title: 'Person added', description: `${p.name} added to registry.` });
    } else {
      toaster.error({ title: 'Failed', description: 'Could not create person.' });
    }
  };

  return (
    <div class="w-full h-full overflow-y-auto p-5 space-y-5 bg-neu-925/20">
      <div class="flex items-center justify-between">
        <div>
          <h2 class="text-base font-semibold text-neu-100 tracking-tight">Person Registry</h2>
          <p class="text-xs text-neu-600 mt-0.5">
            Register known individuals with clear profile photos for face recognition.
            One accepted profile starts recognition; add front/side/angle shots to improve matching quality.
          </p>
        </div>
        <button
          class="flex items-center gap-2 px-3 py-2 rounded-lg bg-violet-600 hover:bg-violet-500 border border-violet-500/40 text-white text-xs font-semibold shadow-sm"
          onClick={() => setAdding(!adding())}
        >
          <FiPlus size={13} /> Add Person
        </button>
      </div>

      <Show when={adding()}>
        <div class="bg-neu-850 border border-violet-700/30 rounded-2xl p-4">
          <p class="text-xs font-semibold text-neu-300 mb-3">New person</p>
          <div class="flex items-center gap-2">
            <input
              class="flex-1 text-sm bg-neu-900 border border-neu-700 focus:border-violet-600 rounded-lg px-3 py-2 text-white outline-none transition-colors"
              placeholder="Full name (e.g. John Doe)"
              value={newName()}
              onInput={(e) => setNewName(e.currentTarget.value)}
              onKeyDown={(e) => { if (e.key === 'Enter') handleCreate(); if (e.key === 'Escape') setAdding(false); }}
            />
            <button
              class="px-4 py-2 rounded-lg bg-violet-700 hover:bg-violet-600 text-white text-xs font-semibold disabled:opacity-50"
              onClick={handleCreate}
              disabled={creating() || !newName().trim()}
            >
              {creating() ? 'Adding...' : 'Add'}
            </button>
            <button
              class="px-3 py-2 rounded-lg bg-neu-800 hover:bg-neu-700 text-neu-400 text-xs"
              onClick={() => setAdding(false)}
            >
              Cancel
            </button>
          </div>
          <p class="text-[11px] text-neu-600 mt-2">
            After adding, upload at least one clear profile photo. Extra angles and varied lighting improve recognition.
          </p>
        </div>
      </Show>

      <UnknownTimelinePanel />

      <Show
        when={persons().length > 0}
        fallback={
          <div class="flex flex-col items-center justify-center py-16 text-center">
            <FiUser size={40} class="text-neu-700 mb-4" />
            <p class="text-neu-500 text-sm">No persons registered yet.</p>
            <p class="text-neu-700 text-xs mt-1">Click "Add Person" to register someone for face recognition.</p>
          </div>
        }
      >
        <div class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-3">
          <For each={persons()}>
            {(p) => <PersonCard person={p} />}
          </For>
        </div>
      </Show>
    </div>
  );
}
