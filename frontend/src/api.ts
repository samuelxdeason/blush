// api.ts is the single data path for the UI. It talks to the Trove server
// over HTTP (+ Server-Sent Events for live updates), and exports the same names
// App.tsx used to import from the Wails bindings, so component code is unchanged.
//
// It runs in two contexts:
//   - desktop (Wails): the server is in-process; its base URL comes from APIBase().
//     Native-only actions (file dialogs, reveal-in-Explorer) use the Wails bindings.
//   - browser (headless daemon): same origin (base ""), and native actions fall
//     back to web equivalents (file upload, window.open).
import * as wails from "../wailsjs/go/main/App";
import { BrowserOpenURL as wailsOpenURL } from "../wailsjs/runtime/runtime";
import { library, downloader } from "../wailsjs/go/models";

type Video = library.Video;

const isWails = !!(window as any).go || typeof (window as any).runtime !== "undefined";

let basePromise: Promise<string> | null = null;
function base(): Promise<string> {
  if (!basePromise) basePromise = isWails ? wails.APIBase() : Promise.resolve("");
  return basePromise;
}

async function getJSON<T>(path: string): Promise<T> {
  const r = await fetch((await base()) + path);
  if (!r.ok) throw new Error(await r.text());
  return r.json();
}
async function postJSON<T = any>(path: string, body?: any): Promise<T> {
  const r = await fetch((await base()) + path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body || {}),
  });
  if (!r.ok) throw new Error(await r.text());
  return r.json();
}
const qs = (o: Record<string, string | number>) =>
  "?" + Object.entries(o).map(([k, v]) => `${k}=${encodeURIComponent(String(v))}`).join("&");

/* ---------------- catalogue reads ---------------- */
export const Models = () => getJSON<library.Model[]>("/api/models");
export const AllLabels = () => getJSON<string[]>("/api/labels");
export const LabelCounts = () => getJSON<library.LabelCount[]>("/api/labelcounts");
export const Favorites = () => getJSON<Video[]>("/api/favorites");
export const RecentlyDownloaded = () => getJSON<Video[]>("/api/recent");
export const RecentlyWatched = () => getJSON<Video[]>("/api/watched");
export const ContinueWatching = () => getJSON<Video[]>("/api/continue");
export const Stats = () => getJSON<library.Stats>("/api/stats");
export const CookieStatus = () => getJSON<{ x: boolean; pornhub: boolean }>("/api/cookiestatus");
export const MediaRootPath = () => getJSON<string>("/api/mediaroot");
export const Collections = () => getJSON<library.Collection[]>("/api/collections");
export const Queue = () => getJSON<downloader.Job[]>("/api/queue");

// AllVideos pages through the whole library (the browse-everything timeline).
// sort: newest | oldest | longest | largest | title | shuffle. site/fav narrow
// the set. seed keeps a "shuffle" order stable across pages within a session.
export const AllVideos = (limit: number, offset: number, sort: string, site: string, fav: boolean, seed = 0) =>
  getJSON<Video[]>("/api/videos" + qs({ limit, offset, sort, site, fav: fav ? 1 : 0, seed }));
export const VideosByModel = (model: string) => getJSON<Video[]>("/api/videos/by-model" + qs({ model }));
export const VideosByLabel = (label: string) => getJSON<Video[]>("/api/videos/by-label" + qs({ label }));
export const VideosByCollection = (id: number) => getJSON<Video[]>("/api/videos/by-collection" + qs({ id }));
export const Search = (q: string) => getJSON<Video[]>("/api/search" + qs({ q }));
export const PhotosByModel = (model: string) => getJSON<library.Photo[]>("/api/photos" + qs({ model }));
export const GetModelInfo = (name: string) => getJSON<library.ModelInfo>("/api/modelinfo" + qs({ name }));
export const Enumerate = (url: string, refresh = false) =>
  getJSON<downloader.RemoteItem[]>("/api/enumerate" + qs({ url, refresh: refresh ? 1 : 0 }));
export const SyncedURLs = () => getJSON<string[]>("/api/synced");
// A saved sync (favorites / channel / list) with when it was fetched and live counts.
export type SyncSummary = { url: string; title: string; kind: string; fetchedAt: string; count: number; owned: number; new: number };
export const SyncedLists = () => getJSON<SyncSummary[]>("/api/synced/lists");
export const RemoveSync = (url: string) => postJSON("/api/synced/remove", { url });
export const CollectionsForVideo = (site: string, id: string) =>
  getJSON<number[]>("/api/collections-for-video" + qs({ site, id }));

/* ---------------- catalogue writes ---------------- */
export const MarkWatched = (site: string, id: string) => postJSON("/api/markwatched", { site, id });
// SetPosition saves a resume point (seconds). duration lets the server clear it once finished.
export const SetPosition = (site: string, id: string, position: number, duration: number) =>
  postJSON("/api/position", { site, id, position, duration });
export const SetModels = (site: string, id: string, models: string[]) => postJSON("/api/setmodels", { site, id, models });
// Strip a model from all its videos (videos with no models left become Unassigned).
export const UnassignModel = (name: string) => postJSON("/api/models/unassign", { name });
export const SetTitle = (site: string, id: string, title: string) => postJSON("/api/settitle", { site, id, title });
export const SetFavorite = (site: string, id: string, fav: boolean) => postJSON("/api/setfavorite", { site, id, fav });
export const SetLabels = (site: string, id: string, labels: string[]) => postJSON("/api/setlabels", { site, id, labels });
export const SetModelCover = (name: string, cover: string) => postJSON("/api/setmodelcover", { name, cover });
export const SaveModelInfo = (name: string, nickname: string, bio: string, links: library.ModelLink[]) =>
  postJSON("/api/savemodelinfo", { name, nickname, bio, links });
// RenameModel renames a person everywhere (videos, photos, profile).
export const RenameModel = (name: string, newName: string) => postJSON("/api/model/rename", { name, newName });
// Verified platform accounts: a profile link to x.com/<handle> or a Pornhub
// pornstar/channel page claims that account. AccountMatches lists claimed
// accounts with how many Unsorted videos await them; ClaimAccount assigns
// those videos to the person. Assigning a video manually implies nothing
// about the account that posted it — only saved links create claims.
export type AccountMatch = { platform: string; handle: string; unsortedCount: number };
export const AccountMatches = (name: string) => getJSON<AccountMatch[]>("/api/model/accountmatches" + qs({ name }));
export const ClaimAccount = (name: string, platform: string, handle: string) =>
  postJSON<{ assigned: number }>("/api/model/claimaccount", { name, platform, handle });

// Appears-in ("featured"): people who are IN a video but didn't upload it —
// kept separate from the owner/collection assignment on purpose.
export const SetFeatured = (site: string, id: string, featured: string[]) =>
  postJSON("/api/setfeatured", { site, id, featured });
export const VideosFeaturing = (model: string) => getJSON<Video[]>("/api/videos/featuring" + qs({ model }));
// CastSuggestions: videos whose downloaded metadata lists the person in the
// cast but aren't linked to them yet; AcceptCast adds one to their appears-in.
export const CastSuggestions = (name: string) => getJSON<Video[]>("/api/model/castsuggestions" + qs({ name }));
export const AcceptCast = (site: string, id: string, name: string) => postJSON("/api/model/acceptcast", { site, id, name });
// Reinterpretation: the accounts-based re-read of every manual assignment.
// The plan's toFeatured/autoFeatured apply automatically; review items wait
// for a human (keep = deliberate save, tofeatured = they only appear in it).
// Platform accounts: the identities that own videos. People connect to
// accounts; badges and the Uploads section derive from connections only.
export type AccountInfo = { platform: string; handle: string; kind: string; displayName: string; url: string; person: string; source: string; firstSeen: string };
export type AccountWithCount = AccountInfo & { videoCount: number };
export const AllAccounts = () => getJSON<AccountInfo[]>("/api/accounts");
export const AccountsWithCounts = () => getJSON<AccountWithCount[]>("/api/accounts?counts=1");
// AdoptAccount: connect an account to a person (creating the person if new),
// claim the Unsorted videos it owns, and resolve its cast appearances.
export const AdoptAccount = (platform: string, handle: string, name: string) =>
  postJSON<Record<string, number>>("/api/accounts/adopt", { platform, handle, name });
export const AccountsForPerson = (name: string) => getJSON<AccountInfo[]>("/api/accounts/for-person" + qs({ name }));
// ConnectAccount("", …) with an empty name disconnects.
export const ConnectAccount = (platform: string, handle: string, name: string) =>
  postJSON("/api/accounts/connect", { platform, handle, name });
// CreateAccount from a pasted profile URL (platform+handle recognised server-side).
export const CreateAccount = (url: string, name: string) => postJSON("/api/accounts/create", { url, name });
export const VideosUploadedBy = (name: string) => getJSON<Video[]>("/api/videos/uploads" + qs({ name }));
export const VideosSavedBy = (name: string) => getJSON<Video[]>("/api/videos/saved" + qs({ name }));

export type ReinterpretAction = { video: Video; person: string; reason: string };
export type ReinterpretPlan = { toFeatured: ReinterpretAction[]; autoFeatured: ReinterpretAction[]; review: ReinterpretAction[] };
export const GetReinterpretPlan = () => getJSON<ReinterpretPlan>("/api/maintenance/reinterpret");
export const ApplyReinterpret = () => postJSON<Record<string, number>>("/api/maintenance/reinterpret/apply");
export const ReinterpretKeep = (site: string, id: string, name: string) => postJSON("/api/maintenance/reinterpret/keep", { site, id, name });
export const ReinterpretToFeatured = (site: string, id: string, name: string) => postJSON("/api/maintenance/reinterpret/tofeatured", { site, id, name });
// Avatar editing: download from a URL, or upload a file. (Picking a video frame
// reuses SetModelCover with that video's thumbnail path.)
export const SetAvatarFromURL = (name: string, url: string) => postJSON("/api/avatar/url", { name, url });
// Auto-fetch a model's avatar from their Pornhub profile (one model, or all at once via SSE "avatar" events).
export const FetchAvatar = (name: string) => postJSON<{ set: boolean }>("/api/avatar/fetch", { name });
export const FetchAllAvatars = () => postJSON("/api/avatars/fetch-all");
export const UploadAvatar = async (name: string, file: File) => {
  const b = await base();
  const form = new FormData();
  form.append("name", name);
  form.append("file", file, file.name);
  const r = await fetch(b + "/api/avatar/upload", { method: "POST", body: form });
  if (!r.ok) throw new Error(await r.text());
  return r.json();
};

/* ---------------- downloads ---------------- */
export const Enqueue = (url: string) => postJSON<string>("/api/enqueue", { url });
export const EnqueueMany = (urls: string[]) => postJSON<{ added: number }>("/api/enqueue/many", { urls });
export const RemoveJob = (id: string) => postJSON("/api/job/remove", { id });
export const ClearFinished = () => postJSON("/api/clearfinished");

/* ---------------- maintenance ---------------- */
// RebuildLibrary re-scans the vault and rebuilds catalogue rows from the media +
// .info.json sidecars (keeps your models/favorites/labels). Returns the count.
export const RebuildLibrary = () => postJSON<{ count: number }>("/api/rebuild");
// OptimizeStreaming losslessly remuxes mp4s whose index sits at the end of the
// file (they stall before playing on phones). Progress arrives via "optimize" SSE.
export const OptimizeStreaming = () => postJSON("/api/optimize");
// BackupCatalogue copies the catalogue db to .trove/backups and returns its path.
export const BackupCatalogue = () => postJSON<{ path: string }>("/api/backup");

/* ---------------- collections ---------------- */
export const CreateCollection = async (name: string, hidden: boolean) =>
  (await postJSON<{ id: number }>("/api/collections/create", { name, hidden })).id;
export const RenameCollection = (id: number, name: string) => postJSON("/api/collections/rename", { id64: id, name });
export const SetCollectionHidden = (id: number, hidden: boolean) => postJSON("/api/collections/hidden", { id64: id, hidden });
export const SetCollectionLocked = (id: number, locked: boolean) => postJSON("/api/collections/locked", { id64: id, locked });
export const DeleteCollection = (id: number) => postJSON("/api/collections/delete", { id64: id });
export const AddToCollection = (id: number, site: string, videoId: string) =>
  postJSON("/api/collections/add", { id64: id, site, videoId });
export const RemoveFromCollection = (id: number, site: string, videoId: string) =>
  postJSON("/api/collections/remove", { id64: id, site, videoId });

/* ---------------- media ---------------- */
// MediaBase returns the server origin to prefix /media URLs with (so the <video>
// and <img> elements load from the API server, not the webview asset scheme).
export const MediaBase = () => base();

/* ---------------- live events (Server-Sent Events) ---------------- */
let esPromise: Promise<EventSource> | null = null;
function eventSource(): Promise<EventSource> {
  if (!esPromise) esPromise = base().then((b) => new EventSource(b + "/api/events"));
  return esPromise;
}

// EventsOn subscribes to a named server event; returns an unsubscribe function.
// Mirrors the Wails runtime EventsOn signature App.tsx already uses.
export function EventsOn(event: string, cb: (data: any) => void): () => void {
  const handler = (e: MessageEvent) => { try { cb(JSON.parse(e.data)); } catch { cb(e.data); } };
  let live = true;
  let src: EventSource | undefined;
  eventSource().then((es) => { if (live) { src = es; es.addEventListener(event, handler as EventListener); } });
  return () => { live = false; src?.removeEventListener(event, handler as EventListener); };
}

/* ---------------- native actions (desktop) with web fallbacks ---------------- */
// isDesktopApp lets the UI hide actions that only make sense with a local
// filesystem (reveal in Explorer, choose folder) when running in a browser.
export const isDesktopApp = isWails;
export const BrowserOpenURL = (url: string) => (isWails ? wailsOpenURL(url) : void window.open(url, "_blank"));
export const OpenFolder = (path: string) => (isWails ? wails.OpenFolder(path) : undefined);
export const ChooseMediaRoot = () => (isWails ? wails.ChooseMediaRoot() : Promise.resolve(""));
export const RestartApp = () => (isWails ? wails.RestartApp() : void location.reload());

// Import takes server-side paths (desktop drag-and-drop). In the browser the
// equivalent is uploadFiles().
export const Import = (paths: string[], model: string) =>
  isWails ? wails.Import(paths, model) : postJSON("/api/import", { paths, model });

// ConnectCookies: desktop opens a native file dialog; browser uploads a cookies.txt.
export const ConnectCookies = async (): Promise<{ x: boolean; pornhub: boolean }> => {
  if (isWails) return wails.ConnectCookies() as any;
  const file = await pickFile(".txt", false);
  if (!file[0]) return CookieStatus();
  const text = await file[0].text();
  const r = await fetch((await base()) + "/api/cookies/upload", { method: "POST", body: text });
  return r.json();
};

export const ImportFilesDialog = (model: string) =>
  isWails ? wails.ImportFilesDialog(model) : uploadFiles(model, "video/*");
export const ImportFolderDialog = () =>
  isWails ? wails.ImportFolderDialog() : uploadFiles("", "video/*");
export const ImportPhotosDialog = (model: string) =>
  isWails ? wails.ImportPhotosDialog(model) : uploadFiles(model, "image/*");

// ImportPhotosFromURL downloads every photo in a web gallery page into the
// model's album (server-side, async; progress via "import" SSE events).
export const ImportPhotosFromURL = (url: string, model: string, album: string) =>
  postJSON("/api/photos/from-url", { url, model, name: album });

// pickFile shows the browser's file chooser and resolves with the chosen files.
function pickFile(accept: string, multiple: boolean): Promise<File[]> {
  return new Promise((resolve) => {
    const input = document.createElement("input");
    input.type = "file";
    input.accept = accept;
    input.multiple = multiple;
    input.onchange = () => resolve(input.files ? Array.from(input.files) : []);
    input.click();
  });
}

// uploadFiles lets the user pick files and uploads each to the server for import.
async function uploadFiles(model: string, accept: string): Promise<void> {
  const files = await pickFile(accept, true);
  const b = await base();
  for (const f of files) {
    const form = new FormData();
    form.append("model", model);
    form.append("file", f, f.name);
    await fetch(b + "/api/upload", { method: "POST", body: form });
  }
}
