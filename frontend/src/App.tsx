import { useCallback, useEffect, useRef, useState } from "react";
import {
  Models, VideosByModel, Search, RecentlyDownloaded, RecentlyWatched, MarkWatched, SetModels, SetTitle,
  SetFavorite, SetLabels, AllLabels, Favorites, LabelCounts, VideosByLabel,
  Enqueue, Enumerate, Queue, RemoveJob, ClearFinished, Import, ImportFilesDialog, ImportFolderDialog,
  PhotosByModel, ImportPhotosDialog, GetModelInfo, SaveModelInfo, SetModelCover,
  CookieStatus, ConnectCookies, OpenFolder,
  MediaRootPath, ChooseMediaRoot, RestartApp, Stats, MediaBase,
} from "../wailsjs/go/main/App";
import { EventsOn, BrowserOpenURL } from "../wailsjs/runtime/runtime";
import { library, downloader } from "../wailsjs/go/models";

type Model = library.Model;
type Video = library.Video;
type Photo = library.Photo;
type ModelInfo = library.ModelInfo;
type ModelLink = library.ModelLink;
type Job = downloader.Job;

const mediaURL = (p?: string) => (p ? `/media?p=${encodeURIComponent(p)}` : "");
// Video streams from a real localhost HTTP server (set once at startup) so
// seeking in long files is reliable; thumbnails keep using the asset path.
let MEDIA_BASE = "";
const videoURL = (p?: string) => (p ? `${MEDIA_BASE}/media?p=${encodeURIComponent(p)}` : "");
const SITE_LABEL: Record<string, string> = { Twitter: "X / Twitter", PornHub: "Pornhub" };
const label = (s: string) => SITE_LABEL[s] || s;
const UNASSIGNED = "Unassigned";
const modelLabel = (name: string) => (name ? name : UNASSIGNED);

type Route =
  | { kind: "library" }
  | { kind: "model"; name: string }
  | { kind: "recent" }
  | { kind: "watched" }
  | { kind: "favorites" }
  | { kind: "categories" }
  | { kind: "category"; label: string }
  | { kind: "browse" }
  | { kind: "downloads" }
  | { kind: "settings" };

function fmtDur(s?: number) {
  if (!s) return "";
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), sec = Math.floor(s % 60);
  const mm = String(m).padStart(h ? 2 : 1, "0");
  return (h ? `${h}:` : "") + `${mm}:${String(sec).padStart(2, "0")}`;
}
function fmtTotal(s?: number) {
  if (!s) return "";
  const h = Math.floor(s / 3600), m = Math.round((s % 3600) / 60);
  return h ? `${h}h ${m}m` : `${m}m`;
}
function fmtSize(b?: number) {
  if (!b) return "";
  const gb = b / 1073741824;
  return gb >= 1 ? `${gb.toFixed(1)} GB` : `${Math.round(b / 1048576)} MB`;
}

const STATUS_COLORS: Record<string, string> = {
  queued: "bg-edge text-muted",
  downloading: "bg-accent text-white",
  done: "bg-emerald-500 text-emerald-950",
  duplicate: "bg-amber-500 text-amber-950",
  error: "bg-rose-600 text-white",
};

function XLogo({ className = "" }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" className={className} fill="currentColor" aria-hidden>
      <path d="M18.244 2.25h3.308l-7.227 8.26 8.502 11.24H16.17l-5.214-6.817L4.99 21.75H1.68l7.73-8.835L1.254 2.25H8.08l4.713 6.231zm-1.161 17.52h1.833L7.084 4.126H5.117z" />
    </svg>
  );
}

function SourceBadge({ site, size = "sm" }: { site: string; size?: "sm" | "lg" }) {
  const big = size === "lg";
  if (site === "PornHub")
    return (
      <span className={`font-extrabold tracking-tight ${big ? "text-2xl" : "text-[13px]"}`}>
        <span className="text-white">Porn</span>
        <span className="bg-[#ff9000] text-black rounded px-1">hub</span>
      </span>
    );
  if (site === "Twitter")
    return (
      <span className={`inline-flex items-center gap-1.5 font-bold ${big ? "text-2xl" : "text-[13px]"}`}>
        <XLogo className={big ? "w-6 h-6" : "w-3.5 h-3.5"} />{!big && <span>X</span>}
      </span>
    );
  return <span className={big ? "text-2xl font-bold" : "text-[13px]"}>{label(site)}</span>;
}

// small inline source tags for a model tile (e.g. PH + X)
function SiteTags({ sites }: { sites: string }) {
  const arr = (sites || "").split(",").filter(Boolean);
  return (
    <span className="inline-flex items-center gap-2">
      {arr.map((s) => <SourceBadge key={s} site={s} />)}
    </span>
  );
}

export default function App() {
  const [models, setModels] = useState<Model[]>([]);
  const [route, setRoute] = useState<Route>({ kind: "library" });
  const [videos, setVideos] = useState<Video[]>([]);
  const [siteFilter, setSiteFilter] = useState<string>("all");
  const [search, setSearch] = useState("");
  const [searchResults, setSearchResults] = useState<Video[] | null>(null);
  const [playing, setPlaying] = useState<Video | null>(null);
  const [queue, setQueue] = useState<Job[]>([]);
  const [importStatus, setImportStatus] = useState<{ done: number; total: number; name?: string; finished?: boolean } | null>(null);

  // Current model context for drag-drop (so a listener always sees the latest route).
  const modelCtx = useRef("");
  modelCtx.current = route.kind === "model" ? route.name : "";

  const accent = siteFilter === "PornHub" ? "#ff9000" : siteFilter === "Twitter" ? "#e7e9ea" : "#ff2d77";
  const modelNames = models.map((m) => m.name).filter(Boolean);
  const [allLabels, setAllLabels] = useState<string[]>([]);
  // Bumped on any data change; all loaders depend on it so views never go stale.
  const [version, setVersion] = useState(0);
  const reload = useCallback(() => setVersion((v) => v + 1), []);

  const loadMeta = useCallback(() => {
    Models().then((m) => setModels(m || []));
    AllLabels().then((l) => setAllLabels(l || []));
  }, []);
  const loadVideos = useCallback(() => {
    if (route.kind === "recent") RecentlyDownloaded().then((v) => setVideos(v || []));
    else if (route.kind === "watched") RecentlyWatched().then((v) => setVideos(v || []));
    else if (route.kind === "favorites") Favorites().then((v) => setVideos(v || []));
    else if (route.kind === "category") VideosByLabel(route.label).then((v) => setVideos(v || []));
  }, [route]);

  useEffect(() => { Queue().then((q) => setQueue(q || [])); MediaBase().then((b) => { MEDIA_BASE = b; }); }, []);
  useEffect(() => { loadMeta(); }, [loadMeta, version]);
  useEffect(() => { loadVideos(); }, [loadVideos, version]);

  useEffect(() => {
    const offQueue = EventsOn("queue", (j: Job[]) => {
      setQueue(j || []);
      if ((j || []).some((x) => x.status === "done")) reload();
    });
    const offProg = EventsOn("progress", (p: Job) =>
      setQueue((q) => q.map((x) => (x.id === p.id ? { ...x, percent: p.percent, speed: p.speed, eta: p.eta } : x))));
    const offDrop = EventsOn("filedrop", (paths: string[]) => {
      if (paths && paths.length) Import(paths, modelCtx.current);
    });
    const offImport = EventsOn("import", (s: any) => {
      setImportStatus(s);
      if (s.finished) { reload(); window.setTimeout(() => setImportStatus(null), 5000); }
    });
    return () => { offQueue(); offProg(); offDrop(); offImport(); };
  }, [reload]);

  const searchTimer = useRef<number>();
  useEffect(() => {
    window.clearTimeout(searchTimer.current);
    if (!search.trim()) { setSearchResults(null); return; }
    searchTimer.current = window.setTimeout(() => Search(search.trim()).then((v) => setSearchResults(v || [])), 200);
  }, [search, version]);

  const go = (r: Route) => { setRoute(r); setSearch(""); };
  const play = (v: Video) => { setPlaying(v); MarkWatched(v.site, v.id); };

  const activeDownloads = queue.filter((j) => j.status === "downloading" || j.status === "queued").length;
  const totalVideos = models.reduce((n, m) => n + m.count, 0);

  return (
    <div className="flex h-full" style={{ ["--ac" as any]: accent }}>
      <nav className="w-56 shrink-0 bg-[#0d0d12]/95 border-r border-edge flex flex-col overflow-y-auto">
        <div className="px-5 py-5 text-xl font-extrabold tracking-tight">
          <span className="bg-gradient-to-r from-[#ff2d77] to-[#ff9000] bg-clip-text text-transparent">Media Vault</span>
        </div>

        <SideLabel>Library</SideLabel>
        <SideItem active={route.kind === "library" || route.kind === "model"} onClick={() => go({ kind: "library" })}>▦ Models</SideItem>
        <SideItem active={route.kind === "favorites"} onClick={() => go({ kind: "favorites" })}>❤ Favorites</SideItem>
        <SideItem active={route.kind === "categories" || route.kind === "category"} onClick={() => go({ kind: "categories" })}>🏷️ Categories</SideItem>
        <SideItem active={route.kind === "recent"} onClick={() => go({ kind: "recent" })}>🕑 Recently added</SideItem>
        <SideItem active={route.kind === "watched"} onClick={() => go({ kind: "watched" })}>▶ Recently watched</SideItem>

        <SideLabel>Get more</SideLabel>
        <SideItem active={route.kind === "browse"} onClick={() => go({ kind: "browse" })}>✨ Sync</SideItem>
        <SideItem active={route.kind === "downloads"} onClick={() => go({ kind: "downloads" })}>
          ↓ Downloads {activeDownloads > 0 && <span style={{ color: "var(--ac)" }}>({activeDownloads})</span>}
        </SideItem>
        <SideItem active={route.kind === "settings"} onClick={() => go({ kind: "settings" })}>⚙ Settings</SideItem>

        <div className="mt-auto px-5 py-4 text-xs text-muted">{totalVideos} videos · {models.length} models</div>
      </nav>

      <main className="flex-1 min-w-0 overflow-y-auto">
        {route.kind === "downloads" ? <Downloads queue={queue} />
          : route.kind === "settings" ? <SettingsPage />
          : route.kind === "browse" ? <BrowseSync onEnqueued={() => go({ kind: "downloads" })} />
          : (
            <div className="p-6">
              <div className="flex items-center gap-4 mb-5">
                {route.kind === "model" && <button onClick={() => go({ kind: "library" })} className="text-muted hover:text-white text-sm">← Models</button>}
                {route.kind === "category" && <button onClick={() => go({ kind: "categories" })} className="text-muted hover:text-white text-sm">← Categories</button>}
                <h1 className="text-xl font-semibold">
                  {searchResults !== null ? "Search"
                    : route.kind === "model" ? modelLabel(route.name)
                    : route.kind === "recent" ? "Recently added"
                    : route.kind === "watched" ? "Recently watched"
                    : route.kind === "favorites" ? "Favorites"
                    : route.kind === "categories" ? "Categories"
                    : route.kind === "category" ? `🏷️ ${route.label}`
                    : "Models"}
                </h1>
                {route.kind === "library" && searchResults === null && (
                  <div className="flex rounded-lg overflow-hidden border border-edge text-xs">
                    <FilterBtn on={siteFilter === "all"} onClick={() => setSiteFilter("all")}>All</FilterBtn>
                    <FilterBtn on={siteFilter === "PornHub"} onClick={() => setSiteFilter("PornHub")}>Pornhub</FilterBtn>
                    <FilterBtn on={siteFilter === "Twitter"} onClick={() => setSiteFilter("Twitter")}>X</FilterBtn>
                  </div>
                )}
                <input value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Search…"
                  className="ml-auto w-64 bg-panel border border-edge rounded-lg px-4 py-2 text-sm outline-none focus:border-accent" />
              </div>

              {searchResults !== null
                ? <VideoArea videos={searchResults} modelNames={modelNames} onPlay={play} onChanged={reload} />
                : route.kind === "library"
                  ? <ModelGrid models={models.filter((m) => siteFilter === "all" || (m.sites || "").includes(siteFilter))}
                      onOpen={(m) => go({ kind: "model", name: m.name })} />
                  : route.kind === "model"
                    ? <ModelPage name={route.name} version={version} modelNames={modelNames} onPlay={play} onChanged={reload} />
                    : route.kind === "categories"
                      ? <CategoriesView onOpen={(label) => go({ kind: "category", label })} />
                      : <VideoArea videos={videos} modelNames={modelNames} onPlay={play} onChanged={reload} />}
            </div>
          )}
      </main>

      {importStatus && (
        <div className="fixed bottom-4 left-1/2 -translate-x-1/2 z-30 bg-panel border border-edge rounded-lg px-4 py-2 text-sm shadow-2xl">
          {importStatus.finished
            ? <span className="text-emerald-400">Imported {importStatus.total} file{importStatus.total === 1 ? "" : "s"} ✓</span>
            : <span>Importing {importStatus.done}/{importStatus.total}… <span className="text-muted">{importStatus.name || ""}</span></span>}
        </div>
      )}

      {playing && (
        <WatchPage key={playing.site + "/" + playing.id} video={playing} allLabels={allLabels} modelNames={modelNames}
          onClose={() => setPlaying(null)} onPlay={play} onChanged={reload}
          onOpenModel={(name) => { setPlaying(null); go({ kind: "model", name }); }} />
      )}
    </div>
  );
}

function SideLabel({ children }: any) {
  return <div className="px-5 pt-4 pb-1 text-[11px] uppercase tracking-wider text-muted/70">{children}</div>;
}
function SideItem({ active, onClick, children }: any) {
  return (
    <button onClick={onClick} style={active ? { borderColor: "var(--ac)" } : undefined}
      className={`text-left px-5 py-2.5 text-sm font-medium transition border-l-2 ${active ? "bg-panel2 text-white" : "border-transparent text-muted hover:text-white hover:bg-panel2/50"}`}>
      {children}
    </button>
  );
}
function FilterBtn({ on, onClick, children }: any) {
  return <button onClick={onClick} style={on ? { background: "var(--ac)", color: "#0a0a0a" } : undefined}
    className={`px-3 py-1.5 ${on ? "font-semibold" : "bg-panel text-muted hover:text-white"}`}>{children}</button>;
}

/* ---------------- Models grid ---------------- */

function ModelGrid({ models, onOpen }: { models: Model[]; onOpen: (m: Model) => void }) {
  if (!models.length) return <Empty>No models yet — download something or sort the Unassigned bucket.</Empty>;
  return (
    <div className="grid gap-4" style={{ gridTemplateColumns: "repeat(auto-fill,minmax(210px,1fr))" }}>
      {models.map((m) => (
        <div key={m.name || "__unassigned"} onClick={() => onOpen(m)} className="tile group">
          <div className="aspect-[4/5]">
            {m.thumbnail
              ? <img src={mediaURL(m.thumbnail)} loading="lazy" decoding="async" className="w-full h-full object-cover" />
              : <div className="w-full h-full grid place-items-center text-muted text-4xl bg-panel2">▦</div>}
          </div>
          <div className="overlay" />
          <div className="absolute top-2 left-2"><SiteTags sites={m.sites} /></div>
          <div className="absolute bottom-0 left-0 right-0 p-3">
            <div className={`font-semibold truncate drop-shadow ${m.name ? "" : "text-amber-400"}`}>{modelLabel(m.name)}</div>
            <div className="text-xs text-white/65 mt-0.5">
              {m.count} video{m.count === 1 ? "" : "s"}{m.totalSeconds ? ` · ${fmtTotal(m.totalSeconds)}` : ""}{m.bytes ? ` · ${fmtSize(m.bytes)}` : ""}
            </div>
          </div>
        </div>
      ))}
    </div>
  );
}

/* ---------------- Categories ---------------- */

function CategoriesView({ onOpen }: { onOpen: (label: string) => void }) {
  const [cats, setCats] = useState<library.LabelCount[]>([]);
  useEffect(() => { LabelCounts().then((c) => setCats(c || [])); }, []);
  if (!cats.length) return <Empty>No categories yet — open a video and add tags to start categorizing.</Empty>;
  return (
    <div className="flex flex-wrap gap-3">
      {cats.map((c) => (
        <button key={c.label} onClick={() => onOpen(c.label)}
          className="flex items-center gap-2 bg-panel border border-edge rounded-full px-4 py-2 text-sm hover:border-accent transition">
          <span className="font-medium">{c.label}</span>
          <span className="text-muted text-xs">{c.count}</span>
        </button>
      ))}
    </div>
  );
}

/* ---------------- Model page (photos + videos) ---------------- */

function ModelPage({ name, version, modelNames, onPlay, onChanged }:
  { name: string; version: number; modelNames: string[]; onPlay: (v: Video) => void; onChanged: () => void }) {
  const [videos, setVideos] = useState<Video[]>([]);
  const [photos, setPhotos] = useState<Photo[]>([]);
  const [lightbox, setLightbox] = useState<number | null>(null);
  const [info, setInfo] = useState<ModelInfo | null>(null);
  const [editing, setEditing] = useState(false);

  const load = useCallback(() => {
    VideosByModel(name).then((v) => setVideos(v || []));
    PhotosByModel(name).then((p) => setPhotos(p || []));
    if (name) GetModelInfo(name).then(setInfo); else setInfo(null);
  }, [name]);
  useEffect(() => { load(); }, [load, version]);
  useEffect(() => { const off = EventsOn("import", (s: any) => { if (s.finished) load(); }); return () => { off(); }; }, [load]);
  const changed = () => { load(); onChanged(); };

  return (
    <>
      {name && (
        <div className="flex gap-5 mb-6">
          {info?.cover && <img src={mediaURL(info.cover)} className="w-36 aspect-[3/4] object-cover rounded-xl shrink-0" />}
          <div className="flex-1 min-w-0">
            {info?.bio && <p className="text-sm text-white/80 whitespace-pre-wrap mb-3">{info.bio}</p>}
            {info && info.links && info.links.length > 0 && (
              <div className="flex flex-wrap gap-2 mb-3">
                {info.links.map((l, i) => (
                  <button key={i} onClick={() => BrowserOpenURL(l.url)} style={{ background: "var(--ac)", color: "#0a0a0a" }}
                    className="text-xs font-semibold px-3 py-1.5 rounded-full">{l.label} ↗</button>
                ))}
              </div>
            )}
            <button onClick={() => setEditing(true)} className="text-xs font-medium px-3 py-1.5 rounded-lg bg-edge hover:bg-panel2">Edit profile</button>
          </div>
        </div>
      )}

      <div className="flex items-center gap-3 mb-3">
        <h2 className="text-sm font-semibold text-muted">Photos{photos.length ? ` (${photos.length})` : ""}</h2>
        <button onClick={() => ImportPhotosDialog(name)} className="text-xs font-medium px-3 py-1.5 rounded-lg bg-edge hover:bg-panel2">Add photos…</button>
      </div>
      {photos.length > 0 && (
        <div className="flex gap-2 overflow-x-auto pb-3 mb-6">
          {photos.map((p, i) => (
            <img key={p.id} src={mediaURL(p.filepath)} loading="lazy" onClick={() => setLightbox(i)}
              className="h-44 rounded-lg object-cover cursor-pointer hover:opacity-80 shrink-0" />
          ))}
        </div>
      )}
      <VideoArea videos={videos} modelNames={modelNames} onPlay={onPlay} onChanged={changed} />
      {lightbox !== null && (
        <Lightbox photos={photos} index={lightbox} onIndex={setLightbox} onClose={() => setLightbox(null)}
          onSetCover={name ? (p) => { SetModelCover(name, p.filepath).then(changed); setLightbox(null); } : undefined} />
      )}
      {editing && info && <ProfileEditor info={info} onClose={() => setEditing(false)} onSaved={() => { setEditing(false); load(); onChanged(); }} />}
    </>
  );
}

function ProfileEditor({ info, onClose, onSaved }: { info: ModelInfo; onClose: () => void; onSaved: () => void }) {
  const [bio, setBio] = useState(info.bio || "");
  const [links, setLinks] = useState<{ label: string; url: string }[]>(
    (info.links || []).map((l) => ({ label: l.label, url: l.url })));
  const setLink = (i: number, k: "label" | "url", v: string) => setLinks((ls) => ls.map((l, idx) => (idx === i ? { ...l, [k]: v } : l)));
  const save = async () => {
    const clean = links.filter((l) => l.url.trim()).map((l) => ({ label: l.label.trim() || "Link", url: l.url.trim() }));
    await SaveModelInfo(info.name, bio.trim(), clean);
    onSaved();
  };
  return (
    <div className="fixed inset-0 bg-black/70 z-50 grid place-items-center" onClick={onClose}>
      <div className="bg-panel border border-edge rounded-xl p-5 w-[28rem] max-h-[85vh] overflow-y-auto" onClick={(e) => e.stopPropagation()}>
        <div className="font-semibold mb-3">Edit {info.name}</div>
        <label className="block text-xs text-muted mb-1">Bio</label>
        <textarea value={bio} onChange={(e) => setBio(e.target.value)} rows={4}
          className="w-full bg-panel2 border border-edge rounded-lg px-3 py-2 text-sm outline-none focus:border-accent mb-4" />
        <label className="block text-xs text-muted mb-1">Links</label>
        <div className="space-y-2 mb-2">
          {links.map((l, i) => (
            <div key={i} className="flex gap-2">
              <input value={l.label} onChange={(e) => setLink(i, "label", e.target.value)} placeholder="OnlyFans"
                className="w-28 bg-panel2 border border-edge rounded px-2 py-1.5 text-sm outline-none focus:border-accent" />
              <input value={l.url} onChange={(e) => setLink(i, "url", e.target.value)} placeholder="https://…"
                className="flex-1 bg-panel2 border border-edge rounded px-2 py-1.5 text-sm outline-none focus:border-accent" />
              <button onClick={() => setLinks((ls) => ls.filter((_, idx) => idx !== i))} className="text-muted hover:text-rose-400 px-1">×</button>
            </div>
          ))}
        </div>
        <button onClick={() => setLinks((ls) => [...ls, { label: "", url: "" }])} className="text-xs text-muted hover:text-white mb-4">+ add link</button>
        <div className="flex justify-end gap-2">
          <button onClick={onClose} className="text-sm text-muted hover:text-white px-3 py-2">Cancel</button>
          <button onClick={save} style={{ background: "var(--ac)", color: "#0a0a0a" }} className="text-sm font-semibold px-4 py-2 rounded-lg">Save</button>
        </div>
      </div>
    </div>
  );
}

function Lightbox({ photos, index, onIndex, onClose, onSetCover }:
  { photos: Photo[]; index: number; onIndex: (i: number) => void; onClose: () => void; onSetCover?: (p: Photo) => void }) {
  const n = photos.length;
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
      if (e.key === "ArrowRight") onIndex((index + 1) % n);
      if (e.key === "ArrowLeft") onIndex((index - 1 + n) % n);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [index, n, onIndex, onClose]);
  return (
    <div className="fixed inset-0 bg-black/95 z-50 flex items-center justify-center" onClick={onClose}>
      <button onClick={(e) => { e.stopPropagation(); onIndex((index - 1 + n) % n); }} className="absolute left-4 text-white/60 hover:text-white text-4xl px-2">‹</button>
      <img src={mediaURL(photos[index].filepath)} onClick={(e) => e.stopPropagation()} className="max-h-[92vh] max-w-[92vw] object-contain" />
      <button onClick={(e) => { e.stopPropagation(); onIndex((index + 1) % n); }} className="absolute right-4 text-white/60 hover:text-white text-4xl px-2">›</button>
      <button onClick={onClose} className="absolute top-4 right-4 text-white/70 hover:text-white text-sm">✕ Close</button>
      {onSetCover && (
        <button onClick={(e) => { e.stopPropagation(); onSetCover(photos[index]); }}
          className="absolute top-4 left-4 text-xs font-semibold px-3 py-1.5 rounded-lg" style={{ background: "var(--ac)", color: "#0a0a0a" }}>
          Set as cover
        </button>
      )}
      <div className="absolute bottom-4 text-muted text-xs">{index + 1} / {n}</div>
    </div>
  );
}

/* ---------------- Video area (grid + bulk select) ---------------- */

function VideoArea({ videos, modelNames, onPlay, onChanged }:
  { videos: Video[]; modelNames: string[]; onPlay: (v: Video) => void; onChanged: () => void }) {
  const [selectMode, setSelectMode] = useState(false);
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [moving, setMoving] = useState(false);
  const key = (v: Video) => v.site + "/" + v.id;

  const toggle = (v: Video) => setPicked((p) => { const n = new Set(p); const k = key(v); n.has(k) ? n.delete(k) : n.add(k); return n; });
  const exit = () => { setSelectMode(false); setPicked(new Set()); };
  const pickedVideos = videos.filter((v) => picked.has(key(v)));

  if (!videos?.length) return <Empty>No videos here.</Empty>;
  return (
    <>
      <div className="flex items-center gap-3 mb-3 text-sm">
        {!selectMode
          ? <button onClick={() => setSelectMode(true)} className="text-muted hover:text-white">Select</button>
          : <>
              <span className="text-muted">{picked.size} selected</span>
              <button onClick={() => setMoving(true)} disabled={!picked.size} style={{ background: "var(--ac)", color: "#0a0a0a" }}
                className="font-semibold px-3 py-1.5 rounded-lg text-xs disabled:opacity-40">Move to model…</button>
              <button onClick={exit} className="text-muted hover:text-white text-xs">Cancel</button>
            </>}
      </div>
      <div className="grid gap-4" style={{ gridTemplateColumns: "repeat(auto-fill,minmax(230px,1fr))" }}>
        {videos.map((v) => (
          <VideoCard key={key(v)} v={v} selectMode={selectMode} selected={picked.has(key(v))}
            onClick={() => (selectMode ? toggle(v) : onPlay(v))} />
        ))}
      </div>
      {moving && (
        <ModelsEditor refs={pickedVideos.map((v) => ({ site: v.site, id: v.id }))} modelNames={modelNames}
          onClose={() => setMoving(false)} onDone={() => { setMoving(false); exit(); onChanged(); }} />
      )}
    </>
  );
}

function VideoCard({ v, onClick, selectMode, selected }:
  { v: Video; onClick: () => void; selectMode?: boolean; selected?: boolean }) {
  return (
    <div onClick={onClick} className="tile group"
      style={selected ? { boxShadow: "0 0 0 2px var(--ac), 0 14px 44px rgba(0,0,0,.65)" } : undefined}>
      <div className="aspect-[4/5]">
        {v.thumbnail
          ? <img src={mediaURL(v.thumbnail)} loading="lazy" decoding="async" className="w-full h-full object-cover" />
          : <div className="w-full h-full grid place-items-center text-muted text-4xl bg-panel2">▶</div>}
      </div>
      <div className="overlay" />
      <div className="absolute top-2 left-2"><SourceBadge site={v.site} /></div>
      {selectMode && (
        <span className={`absolute top-2 right-2 w-6 h-6 grid place-items-center rounded-full text-sm font-bold ${selected ? "" : "border-2 border-white/70 bg-black/30"}`}
          style={selected ? { background: "var(--ac)", color: "#0a0a0a" } : undefined}>{selected ? "✓" : ""}</span>
      )}
      {!selectMode && v.duration ? <span className="absolute top-2 right-2 bg-black/75 text-white text-[11px] px-1.5 py-0.5 rounded">{fmtDur(v.duration)}</span> : null}
      <div className="absolute bottom-0 left-0 right-0 p-3">
        <div className="text-sm font-semibold line-clamp-2 leading-snug drop-shadow">{v.favorite ? <span className="text-rose-400">❤ </span> : null}{v.title || v.uploader}</div>
        <div className="text-xs text-white/60 mt-1 truncate">{(v.models && v.models.length ? v.models.join(", ") : UNASSIGNED)}{v.height ? ` · ${v.height}p` : ""}{v.filesize ? ` · ${fmtSize(v.filesize)}` : ""}</div>
      </div>
    </div>
  );
}

/* ---------------- Models editor (assign one or more models) ---------------- */

function ModelsEditor({ refs, modelNames, initial, onClose, onDone }:
  { refs: { site: string; id: string }[]; modelNames: string[]; initial?: string[]; onClose: () => void; onDone: (models: string[]) => void }) {
  const [models, setModels] = useState<string[]>(initial || []);
  const [input, setInput] = useState("");
  const [busy, setBusy] = useState(false);
  const bulk = refs.length > 1;

  const add = () => { const m = input.trim(); setInput(""); if (m && !models.includes(m)) setModels([...models, m]); };
  const save = async () => {
    setBusy(true);
    const clean = models.map((m) => m.trim()).filter(Boolean);
    await Promise.all(refs.map((r) => SetModels(r.site, r.id, clean)));
    onDone(clean);
  };

  return (
    <div className="fixed inset-0 bg-black/70 z-50 grid place-items-center" onClick={onClose}>
      <div className="bg-panel border border-edge rounded-xl p-5 w-[26rem]" onClick={(e) => e.stopPropagation()}>
        <div className="font-semibold mb-1">{bulk ? `Set models for ${refs.length} videos` : "Models"}</div>
        <p className="text-xs text-muted mb-3">Add one or more models. Type a new name to create it. No models = Unassigned.</p>

        <div className="flex flex-wrap gap-2 mb-3 min-h-[2rem]">
          {models.length === 0 && <span className="text-xs text-muted py-1">No models yet</span>}
          {models.map((m) => (
            <span key={m} className="flex items-center gap-1 bg-panel2 border border-edge rounded-full px-3 py-1 text-sm">
              {m} <button onClick={() => setModels(models.filter((x) => x !== m))} className="text-muted hover:text-rose-400">×</button>
            </span>
          ))}
        </div>

        <div className="flex gap-2">
          <input list="me-models" value={input} autoFocus onChange={(e) => setInput(e.target.value)}
            onKeyDown={(e) => { if (e.key === "Enter") add(); }} placeholder="Add a model…"
            className="flex-1 bg-panel2 border border-edge rounded-lg px-3 py-2 text-sm outline-none focus:border-accent" />
          <button onClick={add} className="text-sm font-medium px-3 py-2 rounded-lg bg-edge hover:bg-panel2">Add</button>
          <datalist id="me-models">{modelNames.map((n) => <option key={n} value={n} />)}</datalist>
        </div>

        <div className="flex justify-end gap-2 mt-4">
          <button onClick={onClose} className="text-sm text-muted hover:text-white px-3 py-2">Cancel</button>
          <button onClick={save} disabled={busy} style={{ background: "var(--ac)", color: "#0a0a0a" }}
            className="text-sm font-semibold px-4 py-2 rounded-lg disabled:opacity-50">Save</button>
        </div>
      </div>
    </div>
  );
}

/* ---------------- Watch page (YouTube-style) ---------------- */

function WatchPage({ video, allLabels, modelNames, onClose, onPlay, onOpenModel, onChanged }:
  { video: Video; allLabels: string[]; modelNames: string[]; onClose: () => void; onPlay: (v: Video) => void; onOpenModel: (name: string) => void; onChanged: () => void }) {
  const [related, setRelated] = useState<Video[]>([]);
  const [editing, setEditing] = useState(false);
  const [tv, setTv] = useState(video.title || "");
  const [fav, setFav] = useState(!!video.favorite);
  const [labels, setLabels] = useState<string[]>(video.labels || []);
  const [models, setModelsState] = useState<string[]>(video.models || []);
  const [newLabel, setNewLabel] = useState("");
  const [moving, setMoving] = useState(false);
  const [copied, setCopied] = useState(false);
  const topRef = useRef<HTMLDivElement>(null);
  const primary = models[0] || "";

  const loadRelated = (m: string) =>
    VideosByModel(m).then((v) => setRelated((v || []).filter((x) => !(x.site === video.site && x.id === video.id))));

  useEffect(() => {
    topRef.current?.scrollTo({ top: 0 });
    loadRelated(primary);
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape" && !editing) onClose(); };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose, editing, primary, video.site, video.id]);

  const saveTitle = async () => {
    const t = tv.trim(); setEditing(false);
    if (t && t !== video.title) { await SetTitle(video.site, video.id, t); video.title = t; onChanged(); }
  };
  const toggleFav = async () => { const nf = !fav; setFav(nf); video.favorite = nf; await SetFavorite(video.site, video.id, nf); onChanged(); };
  const commitLabels = async (next: string[]) => { setLabels(next); video.labels = next; await SetLabels(video.site, video.id, next); onChanged(); };
  const addLabel = () => { const l = newLabel.trim(); setNewLabel(""); if (l && !labels.includes(l)) commitLabels([...labels, l]); };
  const copy = () => { try { navigator.clipboard?.writeText(video.webpage_url); setCopied(true); setTimeout(() => setCopied(false), 1500); } catch {} };

  return (
    <div ref={topRef} className="fixed inset-0 z-40 bg-ink overflow-y-auto">
      <div className="sticky top-0 z-10 bg-ink/95 backdrop-blur border-b border-edge px-5 py-3">
        <button onClick={onClose} className="text-muted hover:text-white text-sm">← Back</button>
      </div>
      <div className="max-w-5xl mx-auto p-6">
        <div className="bg-black rounded-xl overflow-hidden" style={{ height: "70vh" }}>
          <video src={videoURL(video.filepath)} controls autoPlay className="w-full h-full object-contain" />
        </div>

        {editing
          ? <input value={tv} autoFocus onChange={(e) => setTv(e.target.value)} onBlur={saveTitle}
              onKeyDown={(e) => { if (e.key === "Enter") saveTitle(); if (e.key === "Escape") setEditing(false); }}
              className="text-xl font-semibold w-full mt-4 bg-panel2 border border-edge rounded px-2 py-1 outline-none focus:border-accent" />
          : <h1 onClick={() => { setTv(video.title || ""); setEditing(true); }}
              className="text-xl font-semibold mt-4 cursor-text hover:opacity-90">{video.title || video.uploader} <span className="text-muted text-sm">✎</span></h1>}

        <div className="flex flex-wrap items-center gap-x-4 gap-y-2 mt-2 text-sm">
          <div className="flex flex-wrap items-center gap-2">
            {models.length === 0
              ? <span className="text-muted">{UNASSIGNED}</span>
              : models.map((m) => (
                  <button key={m} onClick={() => onOpenModel(m)} className="font-medium text-white/90 hover:text-white">{m}</button>
                ))}
            <button onClick={() => setMoving(true)} title="Edit models" className="text-muted hover:text-white">✎</button>
          </div>
          <span className="text-muted">{[label(video.site), video.height ? `${video.height}p` : "", fmtSize(video.filesize), video.upload_date].filter(Boolean).join("  ·  ")}</span>
          <div className="ml-auto flex items-center gap-3">
            <button onClick={toggleFav} className={`flex items-center gap-1.5 px-3 py-1.5 rounded-full font-medium ${fav ? "bg-rose-600 text-white" : "bg-edge text-muted hover:text-white"}`}>
              {fav ? "❤ Liked" : "♡ Like"}
            </button>
            {video.webpage_url && <button onClick={copy} className="text-muted hover:text-white">{copied ? "Copied!" : "Copy link"}</button>}
            {video.webpage_url && <button onClick={() => BrowserOpenURL(video.webpage_url)} className="text-muted hover:text-white">Source ↗</button>}
            <button onClick={() => OpenFolder(video.filepath)} className="text-muted hover:text-white">Show file</button>
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-2 mt-4">
          <span className="text-muted text-sm">🏷️</span>
          {labels.map((l) => (
            <span key={l} className="flex items-center gap-1 bg-panel2 border border-edge rounded-full px-3 py-1 text-xs">
              {l} <button onClick={() => commitLabels(labels.filter((x) => x !== l))} className="text-muted hover:text-rose-400">×</button>
            </span>
          ))}
          <input list="all-labels" value={newLabel} onChange={(e) => setNewLabel(e.target.value)}
            onKeyDown={(e) => { if (e.key === "Enter") addLabel(); }} placeholder="+ add category"
            className="bg-panel border border-edge rounded-full px-3 py-1 text-xs w-36 outline-none focus:border-accent" />
          <datalist id="all-labels">{allLabels.map((l) => <option key={l} value={l} />)}</datalist>
        </div>

        {related.length > 0 && (
          <div className="mt-8">
            <h2 className="font-semibold mb-3">More from {primary || "this collection"}</h2>
            <div className="grid gap-4" style={{ gridTemplateColumns: "repeat(auto-fill,minmax(200px,1fr))" }}>
              {related.map((v) => <VideoCard key={v.site + "/" + v.id} v={v} onClick={() => onPlay(v)} />)}
            </div>
          </div>
        )}
      </div>
      {moving && (
        <ModelsEditor refs={[{ site: video.site, id: video.id }]} modelNames={modelNames} initial={models}
          onClose={() => setMoving(false)}
          onDone={(m) => { setMoving(false); setModelsState(m); video.models = m; loadRelated(m[0] || ""); onChanged(); }} />
      )}
    </div>
  );
}

/* ---------------- Sync ---------------- */

function BrowseSync({ onEnqueued }: { onEnqueued: () => void }) {
  const [url, setUrl] = useState("");
  const [items, setItems] = useState<downloader.RemoteItem[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [status, setStatus] = useState({ x: false, pornhub: false });
  const [fav, setFav] = useState(localStorage.phUser || "");

  useEffect(() => { CookieStatus().then(setStatus); }, []);

  const loadURL = async (u: string) => {
    u = u.trim(); if (!u) return;
    setLoading(true); setItems(null); setPicked(new Set());
    try { setItems((await Enumerate(u)) || []); } catch { setItems([]); } finally { setLoading(false); }
  };
  const loadFavorites = () => {
    const user = fav.trim(); if (!user) return;
    localStorage.phUser = user;
    loadURL(`https://www.pornhub.com/users/${user}/videos/favorites`);
  };
  const toggle = (u: string) => setPicked((p) => { const n = new Set(p); n.has(u) ? n.delete(u) : n.add(u); return n; });
  const newItems = (items || []).filter((i) => !i.owned);
  const ownedCount = (items || []).length - newItems.length;
  const download = () => { picked.forEach((u) => Enqueue(u)); onEnqueued(); };

  return (
    <div className="p-6 max-w-3xl">
      <h1 className="text-xl font-semibold mb-1">Sync</h1>
      <p className="text-sm text-muted mb-4">Pull in a Pornhub model, channel, or playlist — pick what you want and it downloads into your library.</p>

      <div className="flex gap-2">
        <input value={url} onChange={(e) => setUrl(e.target.value)} onKeyDown={(e) => { if (e.key === "Enter") loadURL(url); }}
          placeholder="https://www.pornhub.com/model/NAME/videos"
          className="flex-1 bg-panel border border-edge rounded-lg px-4 py-3 text-sm outline-none focus:border-accent" />
        <button onClick={() => loadURL(url)} disabled={loading} style={{ background: "var(--ac)", color: "#0a0a0a" }}
          className="font-semibold px-6 rounded-lg disabled:opacity-50">{loading ? "Loading…" : "Load"}</button>
      </div>

      <div className="flex items-center gap-2 mt-3 text-sm">
        <span className="text-muted">★ Your favorites:</span>
        <input value={fav} onChange={(e) => setFav(e.target.value)} onKeyDown={(e) => { if (e.key === "Enter") loadFavorites(); }}
          placeholder="pornhub username" className="w-48 bg-panel border border-edge rounded-lg px-3 py-2 outline-none focus:border-accent" />
        <button onClick={loadFavorites} disabled={!status.pornhub || !fav.trim()}
          className="text-xs font-semibold px-3 py-2 rounded-lg bg-edge hover:bg-panel2 disabled:opacity-40">Sync my favorites</button>
        {!status.pornhub && <span className="text-xs text-amber-400">Connect Pornhub in Downloads first</span>}
      </div>

      {loading && <Empty>Loading list…</Empty>}
      {items && items.length > 0 && (
        <>
          <div className="flex items-center gap-3 mt-6 mb-3 text-sm">
            <span className="font-semibold">{items.length} videos</span>
            <span className="text-muted">· {ownedCount} owned · {newItems.length} new · {picked.size} selected</span>
            <button onClick={() => setPicked(new Set(newItems.map((i) => i.url)))} className="ml-auto text-xs text-muted hover:text-white">Select all new</button>
            <button onClick={download} disabled={!picked.size} style={{ background: "var(--ac)", color: "#0a0a0a" }}
              className="text-xs font-semibold px-4 py-2 rounded-lg disabled:opacity-40">Download {picked.size || ""}</button>
          </div>
          <div className="space-y-1">
            {items.map((it) => (
              <label key={it.url} className={`flex items-center gap-3 px-3 py-2 rounded-lg ${it.owned ? "opacity-50" : "hover:bg-panel2 cursor-pointer"}`}>
                {it.owned
                  ? <span className="text-xs text-emerald-400 w-16 shrink-0">in library</span>
                  : <input type="checkbox" checked={picked.has(it.url)} onChange={() => toggle(it.url)} className="w-4 h-4 accent-pink-500 shrink-0" />}
                <span className="text-sm truncate">{it.title || it.url}</span>
              </label>
            ))}
          </div>
        </>
      )}
      {items && items.length === 0 && !loading && <Empty>No videos found — check the URL, or connect Pornhub for private lists.</Empty>}
    </div>
  );
}

/* ---------------- Settings ---------------- */

function SettingsPage() {
  const [root, setRoot] = useState("");
  const [changed, setChanged] = useState(false);
  const [stats, setStats] = useState<library.Stats | null>(null);

  useEffect(() => { MediaRootPath().then(setRoot); Stats().then(setStats); }, []);
  const change = async () => { const next = await ChooseMediaRoot(); if (next && next !== root) { setRoot(next); setChanged(true); } };
  const siteColor = (s: string) => (s === "PornHub" ? "#ff9000" : s === "Twitter" ? "#e7e9ea" : "#ff2d77");

  return (
    <div className="p-6 max-w-3xl">
      <h1 className="text-xl font-semibold mb-6">Settings</h1>
      <section className="bg-panel border border-edge rounded-xl p-5 mb-6">
        <div className="text-sm font-semibold mb-1">Library location</div>
        <p className="text-xs text-muted mb-3">Where your videos, thumbnails, and catalogue live. Point this at an external drive to take your vault with you.</p>
        <div className="flex items-center gap-2">
          <code className="flex-1 bg-ink border border-edge rounded-lg px-3 py-2 text-sm truncate">{root || "…"}</code>
          <button onClick={change} className="text-sm font-medium px-4 py-2 rounded-lg bg-edge hover:bg-panel2 shrink-0">Change folder…</button>
        </div>
        {changed && (
          <div className="mt-3 flex items-center gap-3 text-sm text-amber-400">
            Saved — restart to use the new location.
            <button onClick={() => RestartApp()} style={{ background: "var(--ac)", color: "#0a0a0a" }} className="font-semibold px-3 py-1.5 rounded-lg">Restart now</button>
          </div>
        )}
      </section>
      <section className="bg-panel border border-edge rounded-xl p-5">
        <div className="text-sm font-semibold mb-3">Storage</div>
        {!stats ? <div className="text-muted text-sm">Loading…</div> : (
          <>
            <div className="flex items-baseline gap-2 mb-4">
              <span className="text-3xl font-bold">{fmtSize(stats.totalBytes)}</span>
              <span className="text-muted text-sm">· {stats.videoCount} videos · {stats.modelCount} models</span>
            </div>
            <div className="space-y-3">
              {(stats.sites || []).map((s) => {
                const pct = stats.totalBytes ? Math.round((s.bytes / stats.totalBytes) * 100) : 0;
                return (
                  <div key={s.site}>
                    <div className="flex items-center justify-between text-sm mb-1">
                      <span className="flex items-center gap-2"><SourceBadge site={s.site} /><span className="text-muted">· {s.count} videos</span></span>
                      <span className="font-medium">{fmtSize(s.bytes)} <span className="text-muted text-xs">({pct}%)</span></span>
                    </div>
                    <div className="h-2 bg-ink rounded-full overflow-hidden"><div className="h-full" style={{ width: pct + "%", background: siteColor(s.site) }} /></div>
                  </div>
                );
              })}
            </div>
          </>
        )}
      </section>
    </div>
  );
}

/* ---------------- Downloads ---------------- */

function ConnRow({ label, on, onConnect }: any) {
  return (
    <div className="flex items-center gap-3 py-1.5">
      <span className="w-28">{label}</span>
      <span className={`text-xs ${on ? "text-emerald-400" : "text-muted"}`}>{on ? "connected ✓" : "not connected"}</span>
      <button onClick={onConnect} className="ml-auto text-xs font-medium px-3 py-1.5 rounded-lg bg-edge hover:bg-panel2">{on ? "Reconnect" : "Connect"}</button>
    </div>
  );
}

function Downloads({ queue }: { queue: Job[] }) {
  const [url, setUrl] = useState("");
  const [status, setStatus] = useState({ x: false, pornhub: false });
  useEffect(() => { CookieStatus().then(setStatus); }, []);
  const connect = async () => setStatus(await ConnectCookies());
  const add = () => { const u = url.trim(); if (!u) return; Enqueue(u); setUrl(""); };

  return (
    <div className="p-6 max-w-3xl">
      <h1 className="text-xl font-semibold mb-4">Downloads</h1>
      <div className="flex gap-2">
        <input value={url} onChange={(e) => setUrl(e.target.value)} onKeyDown={(e) => { if (e.key === "Enter") add(); }}
          placeholder="Paste a video URL and press Enter…"
          className="flex-1 bg-panel border border-edge rounded-lg px-4 py-3 text-sm outline-none focus:border-accent" />
        <button onClick={add} className="bg-accent hover:bg-sky-500 text-white font-semibold px-6 rounded-lg">Add</button>
      </div>

      <div className="mt-6 bg-panel border border-edge rounded-lg p-4">
        <div className="text-sm font-semibold mb-1">Connections</div>
        <p className="text-xs text-muted mb-3 leading-relaxed">
          Connect an account so protected / age-restricted posts and your favorites work.
          Install <b>“Get cookies.txt LOCALLY”</b>, open <b>x.com</b> or <b>pornhub.com</b> while logged in,
          click <b>Export</b>, then connect that file here. Saved in your vault and reused automatically.
        </p>
        <ConnRow label={<SourceBadge site="PornHub" />} on={status.pornhub} onConnect={connect} />
        <ConnRow label={<SourceBadge site="Twitter" />} on={status.x} onConnect={connect} />
      </div>

      <div className="mt-6 bg-panel border border-edge rounded-lg p-4">
        <div className="text-sm font-semibold mb-1">Import from your PC</div>
        <p className="text-xs text-muted mb-3 leading-relaxed">
          Add videos you already have. A whole <b>folder</b> becomes a model (named after the folder); loose files land in
          Unassigned. Or just <b>drag &amp; drop</b> files/folders anywhere in the app — dropping onto a model page files them under that model.
        </p>
        <div className="flex gap-2">
          <button onClick={() => ImportFilesDialog("")} className="text-sm font-medium px-4 py-2 rounded-lg bg-edge hover:bg-panel2">Import files…</button>
          <button onClick={() => ImportFolderDialog()} className="text-sm font-medium px-4 py-2 rounded-lg bg-edge hover:bg-panel2">Import folder…</button>
        </div>
      </div>

      <div className="flex items-center justify-between mt-8 mb-2">
        <h2 className="font-semibold">Queue</h2>
        {queue.some((j) => ["done", "duplicate", "error"].includes(j.status)) &&
          <button onClick={() => ClearFinished()} className="text-xs text-muted hover:text-white">Clear finished</button>}
      </div>
      {queue.length === 0 ? <Empty>Nothing queued.</Empty>
        : <div className="space-y-2">{queue.map((j) => <QueueItem key={j.id} j={j} />)}</div>}
    </div>
  );
}

function QueueItem({ j }: { j: Job }) {
  return (
    <div className="bg-panel border border-edge rounded-lg p-3">
      <div className="flex items-center gap-2">
        <span className="flex-1 truncate text-sm">{j.title || j.url}</span>
        <span className={`text-[11px] px-2 py-0.5 rounded-full ${STATUS_COLORS[j.status] || "bg-edge text-muted"}`}>{j.status}</span>
        {j.status === "queued" && <button onClick={() => RemoveJob(j.id)} className="text-muted hover:text-rose-400 px-1">✕</button>}
      </div>
      {(j.status === "downloading" || j.status === "done") && (
        <div className="h-1.5 bg-ink rounded-full mt-2 overflow-hidden">
          <div className="h-full bg-accent transition-all" style={{ width: `${j.status === "done" ? 100 : j.percent || 0}%` }} />
        </div>
      )}
      <div className="text-[11px] text-muted mt-1.5">
        {j.status === "downloading" && `${(j.percent || 0).toFixed(0)}%${j.speed ? ` · ${j.speed}` : ""}${j.eta ? ` · ETA ${j.eta}` : ""}`}
        {j.status === "done" && `✓ ${j.count} file${j.count === 1 ? "" : "s"} saved`}
        {j.status === "duplicate" && "Already in your library — skipped"}
        {j.status === "error" && <span className="text-rose-400">{j.error}</span>}
      </div>
    </div>
  );
}

function Empty({ children }: { children: any }) {
  return (
    <div className="flex flex-col items-center justify-center text-muted py-24 gap-3">
      <div className="text-5xl opacity-30">◍</div>
      <div className="text-sm text-center max-w-sm leading-relaxed">{children}</div>
    </div>
  );
}
