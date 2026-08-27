import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Models, VideosByModel, AllVideos, Search, RecentlyDownloaded, RecentlyWatched, ContinueWatching, MarkWatched, SetPosition, SetModels, SetTitle,
  SetFavorite, SetLabels, AllLabels, Favorites, LabelCounts, VideosByLabel, UnassignModel,
  Enqueue, EnqueueMany, Enumerate, SyncedLists, RemoveSync, Queue, RemoveJob, ClearFinished, Import, ImportFilesDialog, ImportFolderDialog,
  PhotosByModel, ImportPhotosDialog, GetModelInfo, SaveModelInfo, SetModelCover, SetAvatarFromURL, UploadAvatar, FetchAvatar, FetchAllAvatars,
  CookieStatus, ConnectCookies, OpenFolder,
  MediaRootPath, ChooseMediaRoot, RestartApp, Stats, MediaBase, RebuildLibrary, BackupCatalogue, OptimizeStreaming, isDesktopApp,
  Collections, CreateCollection, RenameCollection, SetCollectionHidden, SetCollectionLocked,
  DeleteCollection, AddToCollection, RemoveFromCollection, VideosByCollection, CollectionsForVideo,
  EventsOn, BrowserOpenURL,
} from "./api";
import type { SyncSummary } from "./api";
import { library, downloader } from "../wailsjs/go/models";

type Model = library.Model;
type Video = library.Video;
type Photo = library.Photo;
type ModelInfo = library.ModelInfo;
type ModelLink = library.ModelLink;
type Collection = library.Collection;
type Job = downloader.Job;

// All media (video + thumbnails) streams from the HTTP server, set once at
// startup, with HTTP range support so seeking in long files is reliable. In the
// browser this is same-origin (""); in the desktop app it's the in-process server.
let MEDIA_BASE = "";
const mediaURL = (p?: string) => (p ? `${MEDIA_BASE}/media?p=${encodeURIComponent(p)}` : "");
const videoURL = (p?: string) => (p ? `${MEDIA_BASE}/media?p=${encodeURIComponent(p)}` : "");
const SITE_LABEL: Record<string, string> = { Twitter: "X / Twitter", PornHub: "Pornhub" };
const label = (s: string) => SITE_LABEL[s] || s;
const UNASSIGNED = "Unsorted";
const modelLabel = (name: string) => (name ? name : UNASSIGNED);

// Flavor presets — applied at runtime by setting --ac-rgb (Tailwind's accent
// token + all glows derive from it), persisted in localStorage.
const ACCENTS: { name: string; rgb: string }[] = [
  { name: "Strawberry", rgb: "236 92 133" },
  { name: "Peach", rgb: "247 128 86" },
  { name: "Blueberry", rgb: "124 108 224" },
  { name: "Matcha", rgb: "106 158 96" },
  { name: "Mango", rgb: "240 158 42" },
];
const applyAccent = (rgb: string) => document.documentElement.style.setProperty("--ac-rgb", rgb);

// Every collection gets its own fruit tint (rotating), so the sidebar reads
// like a well-stocked fridge instead of a file tree.
const COLL_TINTS = ["#F0568C", "#FF8A5C", "#8B7BF0", "#7FB069", "#FFB03A"];
const collTint = (i: number) => COLL_TINTS[i % COLL_TINTS.length];

// Hover previews only make sense where a real pointer can hover — on touch
// screens the old touch handlers swallowed taps and spawned dozens of hidden
// <video> loads while scrolling, which exhausts mobile decoders and made
// perfectly good videos refuse to play until a reload.
const CAN_HOVER = window.matchMedia?.("(hover: hover) and (pointer: fine)").matches ?? true;

type Route =
  | { kind: "home" }
  | { kind: "videos" }
  | { kind: "library" }
  | { kind: "feed" }
  | { kind: "model"; name: string }
  | { kind: "recent" }
  | { kind: "watched" }
  | { kind: "favorites" }
  | { kind: "categories" }
  | { kind: "category"; label: string }
  | { kind: "collection"; id: number; name: string }
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
// Relative time like "3d ago" from a "2006-01-02 15:04:05" stamp.
function fmtAgo(s?: string) {
  if (!s) return "never synced";
  const t = Date.parse(s.replace(" ", "T"));
  if (isNaN(t)) return "";
  const min = Math.round((Date.now() - t) / 60000);
  if (min < 1) return "just now";
  if (min < 60) return `${min}m ago`;
  if (min < 1440) return `${Math.round(min / 60)}h ago`;
  if (min < 43200) return `${Math.round(min / 1440)}d ago`;
  return new Date(t).toLocaleDateString();
}
const SYNC_ICON: Record<string, string> = { favorites: "❤", pornstar: "★", model: "★", channel: "📺", user: "👤", list: "≣" };
const kindIcon = (k: string) => SYNC_ICON[k] || "≣";

// Greeting + flavor emoji for the Home header — a small yogurt touch.
const FLAVOR_EMOJI: Record<string, string> = {
  "236 92 133": "🍓", "247 128 86": "🍑", "124 108 224": "🫐", "106 158 96": "🍵", "240 158 42": "🥭",
};
const flavorEmoji = () => FLAVOR_EMOJI[localStorage.accent] || "🍓";
const greeting = () => {
  const h = new Date().getHours();
  return h < 5 ? "Up late?" : h < 12 ? "Good morning" : h < 18 ? "Good afternoon" : "Good evening";
};

// A video added within the last 3 days is flagged "NEW".
function isNew(added?: string) {
  if (!added) return false;
  const t = Date.parse(added.replace(" ", "T"));
  return !isNaN(t) && Date.now() - t < 3 * 86400000;
}

const STATUS_COLORS: Record<string, string> = {
  queued: "bg-edge text-muted",
  downloading: "bg-accent text-acink",
  done: "bg-emerald-500 text-emerald-950",
  duplicate: "bg-amber-500 text-amber-950",
  error: "bg-rose-600 text-white",
};

// Icon: one consistent hand-rolled stroke set (round caps = soft yogurt look),
// replacing the mismatched emoji that used to fill the nav.
function Icon({ name, className = "w-[18px] h-[18px]" }: { name: string; className?: string }) {
  const p: Record<string, JSX.Element> = {
    home: <><path d="M3.5 10.8 12 3.5l8.5 7.3" /><path d="M5.5 9.5V20a.8.8 0 0 0 .8.8H10v-5.6h4v5.6h3.7a.8.8 0 0 0 .8-.8V9.5" /></>,
    feed: <><circle cx="12" cy="12" r="8.8" /><path d="M10.2 8.9v6.2L15.3 12z" fill="currentColor" stroke="none" /></>,
    people: <><circle cx="9" cy="8.2" r="3.4" /><path d="M3.6 19.8c.5-3.4 2.7-5.3 5.4-5.3s4.9 1.9 5.4 5.3" /><path d="M15.3 5.5a3.4 3.4 0 0 1 0 5.4M17.6 14.9c1.5.9 2.5 2.6 2.8 4.9" /></>,
    heart: <path d="M12 20.2C7.2 16.4 3.8 13.4 3.8 9.8 3.8 7.4 5.7 5.5 8 5.5c1.6 0 3.1.9 4 2.2.9-1.3 2.4-2.2 4-2.2 2.3 0 4.2 1.9 4.2 4.3 0 3.6-3.4 6.6-8.2 10.4z" />,
    tag: <><path d="M3.8 12.3 11.5 20l8.3-8.3V4.2h-7.5L3.8 12.3z" /><circle cx="15.7" cy="8.3" r="1.3" /></>,
    clock: <><circle cx="12" cy="12" r="8.8" /><path d="M12 7.2V12l3.3 2" /></>,
    plus: <path d="M12 5.5v13M5.5 12h13" />,
    spark: <><path d="M12 3.5 13.7 9 19.2 10.7 13.7 12.4 12 17.9 10.3 12.4 4.8 10.7 10.3 9z" /><path d="M18.5 16.5v4M16.5 18.5h4" /></>,
    download: <><path d="M12 4.2v10.6m0 0 4.2-4.2M12 14.8 7.8 10.6" /><path d="M4.8 19.6h14.4" /></>,
    gear: <><circle cx="12" cy="12" r="3.1" /><path d="M12 3v2.6M12 18.4V21M3 12h2.6M18.4 12H21M5.6 5.6l1.9 1.9M16.5 16.5l1.9 1.9M18.4 5.6l-1.9 1.9M7.5 16.5l-1.9 1.9" /></>,
    folder: <path d="M3.8 7.3v10.9a1 1 0 0 0 1 1h14.4a1 1 0 0 0 1-1V9.3a1 1 0 0 0-1-1h-7.6l-2-2H4.8a1 1 0 0 0-1 1z" />,
    hidden: <><path d="M4.2 12.8c2.4 2.3 5 3.4 7.8 3.4s5.4-1.1 7.8-3.4" /><path d="M12 16.6v2.6M7.2 15.5l-1.5 2.2M16.8 15.5l1.5 2.2" /></>,
    lock: <><rect x="5.8" y="11" width="12.4" height="8.6" rx="2" /><path d="M8.7 11V8.2a3.3 3.3 0 0 1 6.6 0V11" /></>,
    menu: <path d="M4.5 7.2h15M4.5 12h15M4.5 16.8h15" />,
    search: <><circle cx="11" cy="11" r="6.6" /><path d="m15.9 15.9 4.1 4.1" /></>,
    grid: <><rect x="4" y="4" width="6.8" height="6.8" rx="1.8" /><rect x="13.2" y="4" width="6.8" height="6.8" rx="1.8" /><rect x="4" y="13.2" width="6.8" height="6.8" rx="1.8" /><rect x="13.2" y="13.2" width="6.8" height="6.8" rx="1.8" /></>,
    x: <path d="M6.2 6.2l11.6 11.6M17.8 6.2 6.2 17.8" />,
    "heart-fill": <path d="M12 20.2C7.2 16.4 3.8 13.4 3.8 9.8 3.8 7.4 5.7 5.5 8 5.5c1.6 0 3.1.9 4 2.2.9-1.3 2.4-2.2 4-2.2 2.3 0 4.2 1.9 4.2 4.3 0 3.6-3.4 6.6-8.2 10.4z" fill="currentColor" stroke="none" />,
    bookmark: <path d="M7 4.6h10a1 1 0 0 1 1 1v13.8l-6-3.9-6 3.9V5.6a1 1 0 0 1 1-1z" />,
    "bookmark-fill": <path d="M7 4.6h10a1 1 0 0 1 1 1v13.8l-6-3.9-6 3.9V5.6a1 1 0 0 1 1-1z" fill="currentColor" stroke="none" />,
    volume: <><path d="M4.6 9.6v4.8h2.9l4.4 3.7V5.9L7.5 9.6z" /><path d="M15.3 9.2a4 4 0 0 1 0 5.6M17.9 6.9a7.4 7.4 0 0 1 0 10.2" /></>,
    "volume-off": <><path d="M4.6 9.6v4.8h2.9l4.4 3.7V5.9L7.5 9.6z" /><path d="m15.4 9.7 4.6 4.6M20 9.7l-4.6 4.6" /></>,
    shuffle: <><path d="M4 7.2h2.9c4.7 0 4.5 9.6 9.2 9.6H19M4 16.8h2.9c1.9 0 3-1.4 3.9-3M19 7.2h-2.9c-1.9 0-3 1.4-3.9 3" /><path d="m16.6 4.8 2.6 2.4-2.6 2.4M16.6 14.4l2.6 2.4-2.6 2.4" /></>,
    "play-fill": <path d="M8.6 5.6v12.8L19.2 12z" fill="currentColor" stroke="none" />,
  };
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.9}
      strokeLinecap="round" strokeLinejoin="round" className={className} aria-hidden>
      {p[name]}
    </svg>
  );
}

// Swirl: the little yogurt-swirl logo mark, tinted by the active flavor.
function Swirl({ className = "w-6 h-6" }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" className={className} aria-hidden>
      <circle cx="12" cy="12" r="10" fill="var(--ac)" />
      <path d="M16.8 8.2a6 6 0 1 0 1 5.2M12 8.8a3.4 3.4 0 1 0 3.3 4.3"
        fill="none" stroke="rgba(255,253,250,.9)" strokeWidth="1.7" strokeLinecap="round" />
    </svg>
  );
}

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
        <span>Porn</span>
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
  const [route, setRoute] = useState<Route>({ kind: "home" });
  const [videos, setVideos] = useState<Video[]>([]);
  const [siteFilter, setSiteFilter] = useState<string>("all");
  const [search, setSearch] = useState("");
  const [searchResults, setSearchResults] = useState<Video[] | null>(null);
  const [playing, setPlaying] = useState<Video | null>(null);
  const [playQueue, setPlayQueue] = useState<Video[]>([]); // lean-back playlist for autoplay-next
  const [queue, setQueue] = useState<Job[]>([]);
  const [importStatus, setImportStatus] = useState<{ done: number; total: number; name?: string; finished?: boolean } | null>(null);

  // Current model context for drag-drop (so a listener always sees the latest route).
  const modelCtx = useRef("");
  modelCtx.current = route.kind === "model" ? route.name : "";

  const modelNames = models.map((m) => m.name).filter(Boolean);
  const [allLabels, setAllLabels] = useState<string[]>([]);
  const [collections, setCollections] = useState<Collection[]>([]);
  const [videoTotal, setVideoTotal] = useState(0); // true distinct count (matches Settings)
  // Bumped on any data change; all loaders depend on it so views never go stale.
  const [version, setVersion] = useState(0);
  const reload = useCallback(() => setVersion((v) => v + 1), []);

  const loadMeta = useCallback(() => {
    Models().then((m) => setModels(m || []));
    AllLabels().then((l) => setAllLabels(l || []));
    Collections().then((c) => setCollections(c || []));
    Stats().then((s) => setVideoTotal(s?.videoCount || 0));
  }, []);
  const loadVideos = useCallback(() => {
    if (route.kind === "recent") RecentlyDownloaded().then((v) => setVideos(v || []));
    else if (route.kind === "watched") RecentlyWatched().then((v) => setVideos(v || []));
    else if (route.kind === "favorites") Favorites().then((v) => setVideos(v || []));
    else if (route.kind === "category") VideosByLabel(route.label).then((v) => setVideos(v || []));
    else if (route.kind === "collection") VideosByCollection(route.id).then((v) => setVideos(v || []));
  }, [route]);

  useEffect(() => {
    if (localStorage.accent) applyAccent(localStorage.accent); // restore chosen accent
    Queue().then((q) => setQueue(q || []));
    MediaBase().then((b) => { MEDIA_BASE = b; reload(); }); // re-render so media URLs pick up the base
  }, [reload]);
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
    // Refresh grids as avatars stream in (and once the bulk run finishes).
    const offAvatar = EventsOn("avatar", (s: any) => { if (s.finished || (s.added && s.added % 5 === 0)) reload(); });
    return () => { offQueue(); offProg(); offDrop(); offImport(); offAvatar(); };
  }, [reload]);

  const searchTimer = useRef<number>();
  useEffect(() => {
    window.clearTimeout(searchTimer.current);
    if (!search.trim()) { setSearchResults(null); return; }
    searchTimer.current = window.setTimeout(() => Search(search.trim()).then((v) => setSearchResults(v || [])), 200);
  }, [search, version]);

  const [newColl, setNewColl] = useState(false);
  const [gate, setGate] = useState<Collection | null>(null); // locked collection awaiting confirm
  const [navOpen, setNavOpen] = useState(false); // mobile drawer

  const go = (r: Route) => { setRoute(r); setSearch(""); setNavOpen(false); setPlaying(null); };
  const play = (v: Video, list?: Video[]) => {
    setPlaying(v);
    setPlayQueue(list && list.length ? list : [v]);
    MarkWatched(v.site, v.id);
  };
  const openCollection = (c: Collection) => {
    if (c.locked) setGate(c);
    else go({ kind: "collection", id: c.id, name: c.name });
  };

  const activeDownloads = queue.filter((j) => j.status === "downloading" || j.status === "queued").length;
  const totalVideos = videoTotal;
  // Re-keys the main content so each route change replays the entrance animation.
  const routeKey = route.kind + ("name" in route ? route.name : "") + ("id" in route ? String(route.id) : "") + ("label" in route ? route.label : ""); // distinct videos (a video tagged to 2 models isn't counted twice)

  return (
    <div className="flex h-full">
      {navOpen && <div className="fixed inset-0 z-[45] bg-black/65 backdrop-blur-sm md:hidden" onClick={() => setNavOpen(false)} />}
      <nav className={`fixed md:static inset-y-0 left-0 z-50 w-72 md:w-56 shrink-0 bg-panel md:bg-panel/90 border-r border-edge flex flex-col overflow-y-auto transition-transform duration-200 rounded-r-3xl md:rounded-none shadow-2xl shadow-black/50 md:shadow-none ${navOpen ? "translate-x-0" : "-translate-x-full"} md:translate-x-0`}
        style={{ paddingTop: "env(safe-area-inset-top)" }}>
        <div className="px-5 py-6 flex items-center gap-2.5">
          <Swirl className="w-7 h-7 shrink-0" />
          <span className="text-lg font-extrabold tracking-tight text-fg">tr<span className="text-accent">o</span>ve</span>
        </div>

        <SideItem icon="home" active={route.kind === "home"} onClick={() => go({ kind: "home" })}>Home</SideItem>
        <SideItem icon="grid" active={["videos", "recent", "watched", "favorites", "categories", "category"].includes(route.kind)}
          onClick={() => go({ kind: "videos" })}>Videos</SideItem>
        <SideItem icon="people" active={route.kind === "library" || route.kind === "model"} onClick={() => go({ kind: "library" })}>People</SideItem>
        <SideItem icon="feed" active={route.kind === "feed"} onClick={() => go({ kind: "feed" })}>Feed</SideItem>

        <div className="flex items-center justify-between px-5 pt-4 pb-1">
          <span className="text-[11px] uppercase tracking-wider text-muted/70">Collections</span>
          <button onClick={() => setNewColl(true)} title="New collection" className="text-muted hover:text-fg text-sm leading-none">＋</button>
        </div>
        {collections.length === 0 && <div className="px-5 py-1 text-xs text-muted/60">None yet</div>}
        {collections.map((c, i) => (
          <SideItem key={c.id} icon={c.hidden ? "hidden" : "folder"} iconColor={collTint(i)}
            active={route.kind === "collection" && route.id === c.id}
            onClick={() => openCollection(c)}>
            <span className="flex items-center gap-1.5 min-w-0">
              <span className="truncate">{c.name}</span>
              <span className="text-muted text-xs">{c.count}</span>
              {c.locked && <Icon name="lock" className="w-3.5 h-3.5 text-muted shrink-0" />}
            </span>
          </SideItem>
        ))}

        <SideLabel>Manage</SideLabel>
        <SideItem icon="spark" active={route.kind === "browse"} onClick={() => go({ kind: "browse" })}>Following</SideItem>
        <SideItem icon="download" active={route.kind === "downloads"} onClick={() => go({ kind: "downloads" })}>
          <span>Downloads {activeDownloads > 0 && <span style={{ color: "var(--ac)" }}>({activeDownloads})</span>}</span>
        </SideItem>
        <SideItem icon="gear" active={route.kind === "settings"} onClick={() => go({ kind: "settings" })}>Settings</SideItem>

        <div className="mt-auto px-5 py-4 text-xs text-muted">{totalVideos} videos · {models.length} people</div>
      </nav>

      <main className="flex-1 min-w-0 overflow-y-auto pb-24 md:pb-0">
        {route.kind !== "feed" && <TopBar search={search} onSearch={setSearch} onMenu={() => setNavOpen(true)} />}
        <div key={search.trim() ? "search" : routeKey} className="rise">
        {search.trim() ? (
          <div className="p-4 md:p-6">
            <SearchResultsPage q={search.trim()} results={searchResults} models={models} allLabels={allLabels}
              collections={collections} modelNames={modelNames} onPlay={play}
              onOpenModel={(name) => go({ kind: "model", name })} onOpenTag={(label) => go({ kind: "category", label })}
              onOpenCollection={openCollection} onChanged={reload} />
          </div>
        ) : route.kind === "home" ? <Home onPlay={play} onOpenModel={(name) => go({ kind: "model", name })} onGo={go} version={version} />
          : route.kind === "videos" ? <VideosPage version={version} modelNames={modelNames} collections={collections} onPlay={play} onChanged={reload} onOpenTags={() => go({ kind: "categories" })} />
          : route.kind === "feed" ? <Feed onOpenModel={(name) => go({ kind: "model", name })} onClose={() => go({ kind: "home" })} collections={collections} allLabels={allLabels} models={models} onChanged={reload} />
          : route.kind === "downloads" ? <Downloads queue={queue} />
          : route.kind === "settings" ? <SettingsPage />
          : route.kind === "browse" ? <BrowseSync onEnqueued={() => go({ kind: "downloads" })} />
          : (
            <div className="p-4 md:p-6">
              <div className="flex items-center flex-wrap gap-3 md:gap-4 mb-5">
                {route.kind === "model" && <button onClick={() => go({ kind: "library" })} className="text-muted hover:text-fg text-sm">← People</button>}
                {["category", "categories", "recent", "watched", "favorites"].includes(route.kind) && (
                  <button onClick={() => go(route.kind === "category" ? { kind: "categories" } : { kind: "videos" })}
                    className="text-muted hover:text-fg text-sm">← {route.kind === "category" ? "Tags" : "Videos"}</button>
                )}
                <h1 className="text-xl font-bold flex items-center gap-2">
                  {route.kind === "model" ? null
                    : route.kind === "recent" ? "Latest"
                    : route.kind === "watched" ? "History"
                    : route.kind === "favorites" ? "Favorites"
                    : route.kind === "categories" ? "Tags"
                    : route.kind === "category" ? <><Icon name="tag" className="w-5 h-5 text-accent" />{route.label}</>
                    : route.kind === "collection" ? null
                    : "People"}
                </h1>
                {["recent", "watched", "favorites", "category"].includes(route.kind) && videos.length > 0 && (
                  <span className="text-xs font-bold px-2.5 py-1 rounded-full bg-accent/15 text-accent">{videos.length}</span>
                )}
                {route.kind === "library" && (
                  <div className="flex rounded-lg overflow-hidden border border-edge text-xs">
                    <FilterBtn on={siteFilter === "all"} onClick={() => setSiteFilter("all")}>All</FilterBtn>
                    <FilterBtn on={siteFilter === "PornHub"} onClick={() => setSiteFilter("PornHub")}>Pornhub</FilterBtn>
                    <FilterBtn on={siteFilter === "Twitter"} onClick={() => setSiteFilter("Twitter")}>X</FilterBtn>
                  </div>
                )}
              </div>

              {route.kind === "library"
                  ? <ModelGrid models={models.filter((m) => siteFilter === "all" || (m.sites || "").includes(siteFilter))}
                      onOpen={(m) => go({ kind: "model", name: m.name })} onChanged={reload} />
                  : route.kind === "model"
                    ? <ModelPage name={route.name} version={version} modelNames={modelNames} onPlay={play} onChanged={reload} />
                    : route.kind === "categories"
                      ? <CategoriesView onOpen={(label) => go({ kind: "category", label })} />
                      : route.kind === "collection"
                        ? <CollectionPage coll={collections.find((c) => c.id === route.id) || { id: route.id, name: route.name, hidden: false, locked: false, count: 0, created: "" } as Collection}
                            videos={videos} modelNames={modelNames} collections={collections} onPlay={play}
                            onChanged={reload} onBack={() => go({ kind: "library" })} />
                        : <VideoArea videos={videos} modelNames={modelNames} collections={collections} onPlay={play} onChanged={reload} />}
            </div>
          )}
        </div>
      </main>

      <TabBar route={route} onGo={go} onMore={() => setNavOpen(true)} navOpen={navOpen} />

      {importStatus && (
        <div className="fixed bottom-20 md:bottom-4 left-1/2 -translate-x-1/2 z-30 bg-panel border border-edge rounded-lg px-4 py-2 text-sm shadow-2xl">
          {importStatus.finished
            ? <span className="text-emerald-400">Imported {importStatus.total} file{importStatus.total === 1 ? "" : "s"} ✓</span>
            : <span>Importing {importStatus.done}/{importStatus.total}… <span className="text-muted">{importStatus.name || ""}</span></span>}
        </div>
      )}

      {playing && (
        <WatchPage key={playing.site + "/" + playing.id} video={playing} queue={playQueue} allLabels={allLabels} modelNames={modelNames}
          collections={collections} onClose={() => setPlaying(null)} onPlay={play} onChanged={reload}
          onOpenModel={(name) => { setPlaying(null); go({ kind: "model", name }); }} />
      )}

      {newColl && (
        <NewCollectionModal onClose={() => setNewColl(false)}
          onCreated={(id, name) => { setNewColl(false); reload(); go({ kind: "collection", id, name }); }} />
      )}

      {gate && (
        <div className="fixed inset-0 bg-black/65 backdrop-blur-sm z-50 grid place-items-center p-4" onClick={() => setGate(null)}>
          <div className="bg-panel border border-edge rounded-xl p-6 w-[92vw] max-w-[22rem] text-center pop" onClick={(e) => e.stopPropagation()}>
            <div className="text-3xl mb-2">🔒</div>
            <div className="font-semibold mb-1">{gate.name} is locked</div>
            <p className="text-xs text-muted mb-4">This collection is kept private. Open it for this session?</p>
            <div className="flex justify-center gap-2">
              <button onClick={() => setGate(null)} className="text-sm text-muted hover:text-fg px-3 py-2">Cancel</button>
              <button onClick={() => { const c = gate; setGate(null); go({ kind: "collection", id: c.id, name: c.name }); }}
                style={{ background: "var(--ac)", color: "var(--ac-ink)" }} className="text-sm font-semibold px-4 py-2 rounded-lg">Open</button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

function SideLabel({ children }: any) {
  return <div className="px-5 pt-4 pb-1 text-[11px] uppercase tracking-wider text-muted/70">{children}</div>;
}
function SideItem({ icon, iconColor, active, onClick, children }: any) {
  return (
    <button onClick={onClick}
      className={`text-left mx-2 px-3.5 py-2.5 rounded-full text-sm transition flex items-center gap-2.5 ${
        active
          ? "bg-accent/20 text-fg font-bold"
          : "text-muted font-medium hover:text-fg hover:bg-panel2"
      }`}>
      {icon && (
        <span className="shrink-0 leading-none" style={iconColor && !active ? { color: iconColor } : undefined}>
          <Icon name={icon} className={`w-[18px] h-[18px] ${active ? "text-accent" : ""}`} />
        </span>
      )}
      {children}
    </button>
  );
}
function FilterBtn({ on, onClick, children }: any) {
  return <button onClick={onClick} style={on ? { background: "var(--ac)", color: "var(--ac-ink)" } : undefined}
    className={`px-3 py-1.5 ${on ? "font-semibold" : "bg-panel text-muted hover:text-fg"}`}>{children}</button>;
}

/* ---------------- Top bar (search from anywhere) ---------------- */

function TopBar({ search, onSearch, onMenu }: { search: string; onSearch: (q: string) => void; onMenu: () => void }) {
  return (
    <div className="sticky top-0 z-20 glass border-b border-edge/70" style={{ paddingTop: "env(safe-area-inset-top)" }}>
      <div className="flex items-center gap-2 px-3 md:px-6 py-2.5">
        <button onClick={onMenu} aria-label="Open menu"
          className="md:hidden w-10 h-10 shrink-0 grid place-items-center rounded-full text-fg text-2xl leading-none active:bg-panel2">≡</button>
        <Swirl className="md:hidden w-6 h-6 shrink-0" />
        <div className="relative flex-1 max-w-2xl mx-auto">
          <Icon name="search" className="w-[18px] h-[18px] absolute left-3.5 top-1/2 -translate-y-1/2 text-muted pointer-events-none" />
          <input value={search} onChange={(e) => onSearch(e.target.value)} enterKeyHint="search"
            placeholder="Search videos, people, tags, collections…"
            className="w-full bg-panel border border-edge rounded-full pl-10 pr-10 py-2.5 text-sm outline-none focus:border-accent" />
          {search && (
            <button onClick={() => onSearch("")} aria-label="Clear search"
              className="absolute right-1.5 top-1/2 -translate-y-1/2 w-8 h-8 grid place-items-center rounded-full text-muted hover:text-fg hover:bg-panel2">✕</button>
          )}
        </div>
      </div>
    </div>
  );
}

/* ---------------- Search results (people + tags + collections + videos) ---------------- */

function SearchResultsPage({ q, results, models, allLabels, collections, modelNames, onPlay, onOpenModel, onOpenTag, onOpenCollection, onChanged }:
  { q: string; results: Video[] | null; models: Model[]; allLabels: string[]; collections: Collection[]; modelNames: string[];
    onPlay: (v: Video, list?: Video[]) => void; onOpenModel: (name: string) => void; onOpenTag: (label: string) => void;
    onOpenCollection: (c: Collection) => void; onChanged: () => void }) {
  const ql = q.toLowerCase();
  const people = models.filter((m) => m.name && m.name.toLowerCase().includes(ql)).slice(0, 24);
  const tags = allLabels.filter((l) => l.toLowerCase().includes(ql)).slice(0, 20);
  const colls = collections.filter((c) => c.name.toLowerCase().includes(ql));
  const empty = results !== null && !results.length && !people.length && !tags.length && !colls.length;

  if (empty) return <Empty icon="🔍">Nothing matches “{q}” — try a person's name, a tag, or a word from the title.</Empty>;
  return (
    <>
      <h1 className="text-xl font-bold mb-5">Results for “{q}”</h1>

      {people.length > 0 && (
        <section className="mb-6">
          <h2 className="text-[13px] font-bold text-muted mb-2.5">People</h2>
          <div className="row flex gap-4 overflow-x-auto pb-2">
            {people.map((p) => (
              <button key={p.name} onClick={() => onOpenModel(p.name)} className="shrink-0 w-[92px] text-center">
                <div className="avatar w-[84px] h-[84px] mx-auto">
                  {p.thumbnail
                    ? <img src={mediaURL(p.thumbnail)} loading="lazy" className="w-full h-full object-cover" />
                    : <div className="w-full h-full grid place-items-center text-2xl text-muted">{(p.name[0] || "?").toUpperCase()}</div>}
                </div>
                <div className="text-xs font-semibold mt-2 truncate">{p.name}</div>
                <div className="text-[11px] text-muted">{p.count} video{p.count === 1 ? "" : "s"}</div>
              </button>
            ))}
          </div>
        </section>
      )}

      {(tags.length > 0 || colls.length > 0) && (
        <section className="mb-6">
          <div className="flex flex-wrap gap-2">
            {tags.map((l) => (
              <button key={"t" + l} onClick={() => onOpenTag(l)}
                className="flex items-center gap-1.5 bg-panel border border-edge rounded-full px-3.5 py-1.5 text-sm hover:border-accent transition">
                <Icon name="tag" className="w-4 h-4 text-accent" />{l}
              </button>
            ))}
            {colls.map((c) => (
              <button key={"c" + c.id} onClick={() => onOpenCollection(c)}
                className="flex items-center gap-1.5 bg-panel border border-edge rounded-full px-3.5 py-1.5 text-sm hover:border-accent transition">
                <Icon name={c.hidden ? "hidden" : "folder"} className="w-4 h-4 text-accent" />{c.name}
                <span className="text-muted text-xs">{c.count}</span>
                {c.locked && <Icon name="lock" className="w-3.5 h-3.5 text-muted" />}
              </button>
            ))}
          </div>
        </section>
      )}

      {results === null
        ? <CardGridSkeleton />
        : results.length > 0 && (
          <section>
            <h2 className="text-[13px] font-bold text-muted mb-2.5">Videos <span className="text-muted/60 font-semibold text-[11px]">{results.length}</span></h2>
            <VideoArea videos={results} modelNames={modelNames} onPlay={onPlay} onChanged={onChanged} />
          </section>
        )}
    </>
  );
}

/* ---------------- Videos (browse-everything timeline) ---------------- */

const VIDEOS_PAGE = 200;
const VIDEO_SORTS = [
  { key: "newest", label: "Newest first" },
  { key: "oldest", label: "Oldest first" },
  { key: "longest", label: "Longest" },
  { key: "largest", label: "Largest file" },
  { key: "title", label: "A – Z" },
];

// monthOf buckets an "added" stamp into a timeline section ("July 2026").
function monthOf(added?: string) {
  const t = added ? Date.parse(added.replace(" ", "T")) : NaN;
  if (isNaN(t)) return "Earlier";
  return new Date(t).toLocaleDateString(undefined, { month: "long", year: "numeric" });
}

function VideosPage({ version, modelNames, collections, onPlay, onChanged, onOpenTags }:
  { version: number; modelNames: string[]; collections: Collection[];
    onPlay: (v: Video, list?: Video[]) => void; onChanged: () => void; onOpenTags: () => void }) {
  const [vids, setVids] = useState<Video[]>([]);
  const [sort, setSort] = useState(localStorage.videoSort || "newest");
  const [site, setSite] = useState("");
  const [fav, setFav] = useState(false);
  const [done, setDone] = useState(false);
  const [loading, setLoading] = useState(true);
  const busy = useRef(false);
  const sentinel = useRef<HTMLDivElement>(null);

  const pickSort = (s: string) => { setSort(s); localStorage.videoSort = s; };

  useEffect(() => {
    let alive = true;
    setLoading(true);
    setDone(false);
    AllVideos(VIDEOS_PAGE, 0, sort, site, fav).then((v) => {
      if (!alive) return;
      setVids(v || []);
      setDone(!v || v.length < VIDEOS_PAGE);
      setLoading(false);
    });
    return () => { alive = false; };
  }, [sort, site, fav, version]);

  const more = useCallback(async () => {
    if (busy.current || done || loading) return;
    busy.current = true;
    try {
      const v = await AllVideos(VIDEOS_PAGE, vids.length, sort, site, fav);
      setVids((cur) => [...cur, ...(v || [])]);
      if (!v || v.length < VIDEOS_PAGE) setDone(true);
    } finally { busy.current = false; }
  }, [vids.length, sort, site, fav, done, loading]);

  // Infinite scroll: fetch the next page when the sentinel nears the viewport.
  useEffect(() => {
    const el = sentinel.current;
    if (!el) return;
    const io = new IntersectionObserver((es) => { if (es[0].isIntersecting) more(); }, { rootMargin: "800px" });
    io.observe(el);
    return () => io.disconnect();
  }, [more]);

  // Month sections only make sense for date order; other sorts get a flat grid.
  const groups = useMemo(() => {
    if (sort !== "newest" && sort !== "oldest") return undefined;
    const out: { title: string; videos: Video[] }[] = [];
    for (const v of vids) {
      const t = monthOf(v.added);
      if (!out.length || out[out.length - 1].title !== t) out.push({ title: t, videos: [] });
      out[out.length - 1].videos.push(v);
    }
    return out;
  }, [vids, sort]);

  return (
    <div className="p-4 md:p-6">
      <div className="flex items-center flex-wrap gap-2.5 mb-4">
        <h1 className="text-xl font-bold mr-1">Videos</h1>
        <div className="flex rounded-lg overflow-hidden border border-edge text-xs">
          <FilterBtn on={site === ""} onClick={() => setSite("")}>All</FilterBtn>
          <FilterBtn on={site === "PornHub"} onClick={() => setSite("PornHub")}>Pornhub</FilterBtn>
          <FilterBtn on={site === "Twitter"} onClick={() => setSite("Twitter")}>X</FilterBtn>
        </div>
        <button onClick={() => setFav(!fav)}
          className={`text-xs font-semibold px-3 py-1.5 rounded-lg border transition ${fav ? "border-transparent" : "bg-panel border-edge text-muted hover:text-fg"}`}
          style={fav ? { background: "var(--ac)", color: "var(--ac-ink)" } : undefined}>❤ Favorites</button>
        <button onClick={onOpenTags} className="text-xs font-semibold px-3 py-1.5 rounded-lg bg-panel border border-edge text-muted hover:text-fg">
          # Tags
        </button>
        <select value={sort} onChange={(e) => pickSort(e.target.value)} aria-label="Sort"
          className="ml-auto bg-panel border border-edge rounded-lg px-2.5 py-1.5 text-xs text-fg outline-none focus:border-accent">
          {VIDEO_SORTS.map((s) => <option key={s.key} value={s.key}>{s.label}</option>)}
        </select>
      </div>

      {loading
        ? <CardGridSkeleton />
        : vids.length === 0
          ? <Empty icon="🎬">{fav ? "No favorites match this filter yet — tap ❤ on a video you love." : "No videos here yet — add a download or import your files."}</Empty>
          : (
            <>
              <VideoArea videos={vids} groups={groups} modelNames={modelNames} collections={collections} onPlay={onPlay} onChanged={onChanged} />
              {!done && (
                <div className="mt-6 text-center">
                  <div ref={sentinel} className="h-2" />
                  <button onClick={more} className="text-sm font-medium px-5 py-2 rounded-full bg-panel2 hover:bg-edge text-fg border border-edge">Load more</button>
                </div>
              )}
            </>
          )}
    </div>
  );
}

/* ---------------- Home (discovery wall) ---------------- */

function Home({ onPlay, onOpenModel, onGo, version }:
  { onPlay: (v: Video, list?: Video[]) => void; onOpenModel: (name: string) => void; onGo: (r: Route) => void; version: number }) {
  const [recent, setRecent] = useState<Video[]>([]);
  const [cont, setCont] = useState<Video[]>([]);
  const [favs, setFavs] = useState<Video[]>([]);
  const [people, setPeople] = useState<Model[]>([]);
  const [hero, setHero] = useState<Video | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let alive = true;
    setLoading(true);
    Promise.all([RecentlyDownloaded(), ContinueWatching(), Favorites(), Models()]).then(([r, c, f, m]) => {
      if (!alive) return;
      setRecent(r || []); setCont(c || []); setFavs(f || []); setPeople(m || []);
      setLoading(false);
    });
    return () => { alive = false; };
  }, [version]);

  // De-duped pool across favorites/in-progress/recent for the hero + shuffle.
  const pool = useMemo(() => {
    const seen = new Set<string>(); const out: Video[] = [];
    for (const v of [...favs, ...cont, ...recent]) {
      const k = v.site + "/" + v.id;
      if (!seen.has(k)) { seen.add(k); out.push(v); }
    }
    return out;
  }, [favs, cont, recent]);

  // Pick a hero once data arrives (a favorite if any, else a recent), keep it stable.
  useEffect(() => {
    if (hero || pool.length === 0) return;
    const top = favs.length ? favs : recent;
    if (top.length) setHero(top[Math.floor(Math.random() * top.length)]);
  }, [pool, favs, recent, hero]);

  const shuffle = () => { if (pool.length) onPlay(pool[Math.floor(Math.random() * pool.length)], pool); };

  if (loading) return <HomeSkeleton />;
  if (!pool.length && !people.length)
    return <div className="p-6"><Empty icon="🍦">Nothing here yet — follow someone or add a download and the wall fills itself.</Empty></div>;

  const topPeople = people.filter((p) => p.name).slice(0, 18);

  return (
    <div className="pb-10">
      <div className="px-4 md:px-9 pt-4 md:pt-5 flex items-baseline gap-2.5 flex-wrap">
        <span className="text-2xl leading-none">{flavorEmoji()}</span>
        <span className="text-xl font-extrabold tracking-tight text-fg">{greeting()}</span>
        <span className="text-sm font-semibold text-muted">here's what's fresh.</span>
      </div>
      {hero && <Hero v={hero} onPlay={(v) => onPlay(v, pool)} onShuffle={shuffle} />}
      <div className="mt-6 space-y-8">
        {cont.length > 0 && (
          <Row title="Continue watching" onSeeAll={() => onGo({ kind: "watched" })}>
            {cont.slice(0, 18).map((v) => (
              <RowCard key={v.site + v.id} v={v}
                progress={v.position && v.duration ? Math.min(1, v.position / v.duration) : 0}
                onClick={() => onPlay(v, cont)} />
            ))}
          </Row>
        )}
        {topPeople.length > 0 && (
          <section>
            <RowHeader title="Top people" onSeeAll={() => onGo({ kind: "library" })} />
            <div className="row flex gap-4 overflow-x-auto px-4 md:px-8 pt-2 pb-3">
              {topPeople.map((p) => (
                <button key={p.name} onClick={() => onOpenModel(p.name)} className="shrink-0 w-[104px] text-center">
                  <div className="avatar w-[96px] h-[96px] mx-auto">
                    {p.thumbnail
                      ? <img src={mediaURL(p.thumbnail)} loading="lazy" className="w-full h-full object-cover" />
                      : <div className="w-full h-full grid place-items-center text-2xl text-muted">{(p.name[0] || "?").toUpperCase()}</div>}
                  </div>
                  <div className="text-xs font-semibold mt-2 truncate">{p.name}</div>
                  <div className="text-[11px] text-muted">{p.count} video{p.count === 1 ? "" : "s"}</div>
                </button>
              ))}
            </div>
          </section>
        )}
        {recent.length > 0 && (
          <Row title="Newly added" onSeeAll={() => onGo({ kind: "recent" })}>
            {recent.slice(0, 18).map((v) => <RowCard key={v.site + v.id} v={v} onClick={() => onPlay(v, recent)} />)}
          </Row>
        )}
        {favs.length > 0 && (
          <Row title="Your favorites" onSeeAll={() => onGo({ kind: "favorites" })}>
            {favs.slice(0, 18).map((v) => <RowCard key={v.site + v.id} v={v} onClick={() => onPlay(v, favs)} />)}
          </Row>
        )}
      </div>
    </div>
  );
}

function Hero({ v, onPlay, onShuffle }: { v: Video; onPlay: (v: Video) => void; onShuffle: () => void }) {
  return (
    <div className="px-3 md:px-8 pt-3 md:pt-4">
      <div className="relative h-[40vh] min-h-[280px] md:h-[52vh] w-full overflow-hidden rounded-blob md:rounded-[2rem] shadow-xl shadow-black/45">
        {v.thumbnail
          ? <img src={mediaURL(v.thumbnail)} className="absolute inset-0 w-full h-full object-cover" />
          : <div className="absolute inset-0 bg-panel2" />}
        <div className="hero-scrim" />
        <div className="absolute bottom-0 left-0 right-0 p-5 md:p-10 max-w-3xl">
          <span className="swirl-chip inline-block text-[10px] uppercase tracking-widest font-extrabold mb-2.5 px-2.5 py-1 rounded-full">Featured</span>
          <h1 className="text-2xl md:text-4xl font-extrabold leading-tight line-clamp-2 cap text-white">{v.title || v.uploader}</h1>
          <div className="text-sm text-white/80 mt-2 cap">
            {v.models && v.models.length ? v.models.join(", ") : UNASSIGNED}
            {v.height ? ` · ${v.height}p` : ""}{v.duration ? ` · ${fmtDur(v.duration)}` : ""}
          </div>
          <div className="flex gap-3 mt-5">
            <button onClick={() => onPlay(v)} className="glow-btn px-6 py-2.5 text-sm flex items-center gap-2">▶ Play</button>
            <button onClick={onShuffle} className="px-5 py-2.5 text-sm font-semibold rounded-full glass text-fg flex items-center gap-2 hover:bg-panel">⤮ Shuffle</button>
          </div>
        </div>
      </div>
    </div>
  );
}

function RowHeader({ title, onSeeAll }: { title: string; onSeeAll?: () => void }) {
  return (
    <div className="flex items-center justify-between px-4 md:px-8 mb-3">
      <h2 className="text-lg font-bold tracking-tight flex items-center gap-2">
        <span className="w-1.5 h-1.5 rounded-full bg-accent inline-block" />{title}
      </h2>
      {onSeeAll && <button onClick={onSeeAll} className="text-xs text-muted hover:text-fg">See all →</button>}
    </div>
  );
}

function Row({ title, onSeeAll, children }: { title: string; onSeeAll?: () => void; children: any }) {
  return (
    <section>
      <RowHeader title={title} onSeeAll={onSeeAll} />
      <div className="row flex gap-3 md:gap-4 overflow-x-auto px-4 md:px-8 pt-2 pb-3">{children}</div>
    </section>
  );
}

function RowCard({ v, onClick, progress }: { v: Video; onClick: () => void; progress?: number }) {
  return (
    <button onClick={onClick} className="tile shrink-0 w-[230px] md:w-[260px] text-left">
      <PreviewMedia v={v} ratio="aspect-video" />
      <div className="overlay" />
      <div className="absolute top-2 left-2 flex items-center gap-1.5">
        <span className="bg-black/50 text-white backdrop-blur-sm rounded-md px-1.5 py-0.5"><SourceBadge site={v.site} /></span>
        {isNew(v.added) && <span className="swirl-chip text-[10px] font-extrabold px-2 py-0.5 rounded-full">NEW</span>}
      </div>
      {v.duration ? <span className="absolute top-2 right-2 bg-black/75 text-white text-[11px] px-1.5 py-0.5 rounded">{fmtDur(v.duration)}</span> : null}
      <div className="playbtn"><span className="w-12 h-12 grid place-items-center rounded-full glass text-fg text-lg">▶</span></div>
      <div className="absolute bottom-0 left-0 right-0 p-3">
        <div className="text-sm font-semibold line-clamp-1 text-white cap">{v.favorite ? <span className="text-rose-300">❤ </span> : null}{v.title || v.uploader}</div>
        <div className="text-[11px] text-white/80 mt-0.5 truncate cap">{v.models && v.models.length ? v.models.join(", ") : UNASSIGNED}</div>
      </div>
      {progress != null && progress > 0 && <div className="progress"><i style={{ width: `${Math.round(progress * 100)}%` }} /></div>}
    </button>
  );
}

// PreviewMedia shows the thumbnail, then silently plays a muted clip on hover
// after a short delay — desktop pointers only. On touch screens a tap simply
// opens the video: no long-press previews, no hidden full-size video loads.
function PreviewMedia({ v, ratio }: { v: Video; ratio: string }) {
  const [preview, setPreview] = useState(false);
  const timer = useRef<number | undefined>(undefined);
  const start = () => { if (!CAN_HOVER || localStorage.hoverPreview === "0") return; timer.current = window.setTimeout(() => setPreview(true), 450); };
  const stop = () => { window.clearTimeout(timer.current); setPreview(false); };
  return (
    <div className={`relative ${ratio}`} onMouseEnter={start} onMouseLeave={stop}>
      {v.thumbnail
        ? <img src={mediaURL(v.thumbnail)} loading="lazy" decoding="async" className="w-full h-full object-cover" />
        : <div className="w-full h-full grid place-items-center text-muted text-3xl bg-panel2">▶</div>}
      {preview && v.filepath && (
        <video src={videoURL(v.filepath)} autoPlay muted loop playsInline
          className="absolute inset-0 w-full h-full object-cover" />
      )}
    </div>
  );
}

function HomeSkeleton() {
  return (
    <div className="pb-10">
      <div className="px-3 md:px-8 pt-3 md:pt-6"><div className="skel h-[40vh] min-h-[280px] md:h-[52vh]" /></div>
      {[0, 1].map((r) => (
        <div key={r} className="mt-8">
          <div className="skel h-5 w-40 mx-4 md:mx-8 mb-3" />
          <div className="flex gap-4 px-4 md:px-8 overflow-hidden">
            {Array.from({ length: 6 }).map((_, i) => <div key={i} className="skel shrink-0 w-[260px] aspect-video" />)}
          </div>
        </div>
      ))}
    </div>
  );
}

/* ---------------- Models grid ---------------- */

function ModelGrid({ models, onOpen, onChanged }: { models: Model[]; onOpen: (m: Model) => void; onChanged: () => void }) {
  const [selectMode, setSelectMode] = useState(false);
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState(false);
  if (!models.length) return <Empty icon="🍨">No people yet — download something and they'll show up here.</Empty>;

  const toggle = (name: string) => setPicked((p) => { const n = new Set(p); n.has(name) ? n.delete(name) : n.add(name); return n; });
  const exit = () => { setSelectMode(false); setPicked(new Set()); };
  const unassignPicked = async () => {
    if (!picked.size) return;
    setBusy(true);
    for (const name of picked) await UnassignModel(name);
    setBusy(false); exit(); onChanged();
  };

  return (
    <>
      <div className="flex items-center gap-3 mb-3 text-sm">
        {!selectMode
          ? <button onClick={() => setSelectMode(true)} className="text-muted hover:text-fg">Select</button>
          : <>
              <span className="text-muted">{picked.size} selected</span>
              <button onClick={unassignPicked} disabled={!picked.size || busy}
                className="font-medium px-3 py-1.5 rounded-lg text-xs bg-panel2 hover:bg-edge text-fg border border-edge disabled:opacity-40">
                Move videos to Unsorted
              </button>
              <button onClick={exit} className="text-muted hover:text-fg text-xs">Cancel</button>
            </>}
        {selectMode && <span className="text-muted text-xs">Pick the people to clear — their videos move to Unsorted and they disappear from this page.</span>}
      </div>
    <div className="grid gap-x-4 gap-y-6 md:gap-y-7" style={{ gridTemplateColumns: "repeat(auto-fill,minmax(clamp(96px,26vw,150px),1fr))" }}>
      {models.map((m) => {
        const canSelect = selectMode && !!m.name; // can't unassign the Unassigned bucket
        const sel = picked.has(m.name);
        return (
        <button key={m.name || "__unassigned"} onClick={() => (canSelect ? toggle(m.name) : selectMode ? undefined : onOpen(m))}
          className={`flex flex-col items-center text-center ${selectMode && !m.name ? "opacity-40" : ""}`}>
          <div className="avatar w-full aspect-square relative"
            style={sel ? { boxShadow: "0 0 0 3px var(--ac), 0 14px 30px rgba(0,0,0,.55)" } : undefined}>
            {m.thumbnail
              ? <img src={mediaURL(m.thumbnail)} loading="lazy" decoding="async" className="w-full h-full object-cover" />
              : <div className="w-full h-full grid place-items-center text-3xl text-muted">{m.name ? m.name[0].toUpperCase() : "★"}</div>}
            {canSelect && (
              <span className={`absolute top-1 right-1 w-6 h-6 grid place-items-center rounded-full text-sm font-bold ${sel ? "" : "border-2 border-white/70 bg-black/40"}`}
                style={sel ? { background: "var(--ac)", color: "var(--ac-ink)" } : undefined}>{sel ? "✓" : ""}</span>
            )}
          </div>
          <div className={`font-semibold text-sm mt-2.5 truncate w-full px-1 ${m.name ? "text-fg" : "text-accent"}`}>{modelLabel(m.name)}</div>
          <div className="text-[11px] text-muted">{m.count} video{m.count === 1 ? "" : "s"}</div>
        </button>
        );
      })}
    </div>
    </>
  );
}

/* ---------------- Categories ---------------- */

function CategoriesView({ onOpen }: { onOpen: (label: string) => void }) {
  const [cats, setCats] = useState<library.LabelCount[]>([]);
  useEffect(() => { LabelCounts().then((c) => setCats(c || [])); }, []);
  if (!cats.length) return <Empty icon="🏷️">No tags yet — open any video and add a tag to start sorting by mood.</Empty>;
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

/* ---------------- Collections ---------------- */

function NewCollectionModal({ onClose, onCreated }: { onClose: () => void; onCreated: (id: number, name: string) => void }) {
  const [name, setName] = useState("");
  const [hidden, setHidden] = useState(false);
  const [busy, setBusy] = useState(false);
  const create = async () => {
    const n = name.trim();
    if (!n) return;
    setBusy(true);
    const id = await CreateCollection(n, hidden);
    onCreated(id, n);
  };
  return (
    <div className="fixed inset-0 bg-black/65 backdrop-blur-sm z-50 grid place-items-center p-4" onClick={onClose}>
      <div className="bg-panel border border-edge rounded-xl p-5 w-[92vw] max-w-[24rem]" onClick={(e) => e.stopPropagation()}>
        <div className="font-semibold mb-3">New collection</div>
        <input value={name} autoFocus onChange={(e) => setName(e.target.value)}
          onKeyDown={(e) => { if (e.key === "Enter") create(); }} placeholder="e.g. Private, Travel 2026, Memes…"
          className="w-full bg-panel2 border border-edge rounded-lg px-3 py-2 text-sm outline-none focus:border-accent mb-3" />
        <label className="flex items-start gap-2 text-sm mb-1 cursor-pointer">
          <input type="checkbox" checked={hidden} onChange={(e) => setHidden(e.target.checked)} className="mt-1 accent-[color:var(--ac)]" />
          <span>Hidden<span className="block text-xs text-muted">Its videos won't show in the library, search, or model grids — only on this collection's page.</span></span>
        </label>
        <div className="flex justify-end gap-2 mt-4">
          <button onClick={onClose} className="text-sm text-muted hover:text-fg px-3 py-2">Cancel</button>
          <button onClick={create} disabled={busy || !name.trim()} style={{ background: "var(--ac)", color: "var(--ac-ink)" }}
            className="text-sm font-semibold px-4 py-2 rounded-lg disabled:opacity-50">Create</button>
        </div>
      </div>
    </div>
  );
}

// AddToCollectionModal toggles membership of one or more videos across collections.
function AddToCollectionModal({ refs, collections, onClose, onChanged }:
  { refs: { site: string; id: string }[]; collections: Collection[]; onClose: () => void; onChanged: () => void }) {
  const [picked, setPicked] = useState<Set<number>>(new Set());
  const [busy, setBusy] = useState(false);
  const single = refs.length === 1;

  // For a single video, preselect the collections it's already in.
  useEffect(() => {
    if (single) CollectionsForVideo(refs[0].site, refs[0].id).then((ids) => setPicked(new Set(ids || [])));
  }, []);

  const toggle = (id: number) => setPicked((p) => { const n = new Set(p); n.has(id) ? n.delete(id) : n.add(id); return n; });
  const save = async () => {
    setBusy(true);
    for (const c of collections) {
      const want = picked.has(c.id);
      // Single video: sync exact membership. Bulk: only add to picked (never remove).
      if (want) await Promise.all(refs.map((r) => AddToCollection(c.id, r.site, r.id)));
      else if (single) await RemoveFromCollection(c.id, refs[0].site, refs[0].id);
    }
    onChanged();
    onClose();
  };

  return (
    <div className="fixed inset-0 bg-black/65 backdrop-blur-sm z-50 grid place-items-center p-4" onClick={onClose}>
      <div className="bg-panel border border-edge rounded-xl p-5 w-[92vw] max-w-[24rem]" onClick={(e) => e.stopPropagation()}>
        <div className="font-semibold mb-1">{single ? "Add to collections" : `Add ${refs.length} videos to…`}</div>
        <p className="text-xs text-muted mb-3">{single ? "Tick the collections this video belongs to." : "Ticked collections get these videos added."}</p>
        {collections.length === 0
          ? <p className="text-sm text-muted py-2">No collections yet — create one from the sidebar.</p>
          : (
            <div className="space-y-1 max-h-64 overflow-y-auto mb-2">
              {collections.map((c, i) => (
                <label key={c.id} className="flex items-center gap-3 px-2 py-1.5 rounded-lg hover:bg-panel2 cursor-pointer">
                  <input type="checkbox" checked={picked.has(c.id)} onChange={() => toggle(c.id)} className="w-4 h-4 accent-[color:var(--ac)]" />
                  <span className="text-sm flex items-center gap-2 min-w-0">
                    <span style={{ color: collTint(i) }}><Icon name={c.hidden ? "hidden" : "folder"} className="w-4 h-4" /></span>
                    <span className="truncate">{c.name}</span>
                    {c.locked && <Icon name="lock" className="w-3.5 h-3.5 text-muted shrink-0" />}
                  </span>
                  <span className="ml-auto text-xs text-muted">{c.count}</span>
                </label>
              ))}
            </div>
          )}
        <div className="flex justify-end gap-2 mt-3">
          <button onClick={onClose} className="text-sm text-muted hover:text-fg px-3 py-2">Cancel</button>
          <button onClick={save} disabled={busy || !collections.length} style={{ background: "var(--ac)", color: "var(--ac-ink)" }}
            className="text-sm font-semibold px-4 py-2 rounded-lg disabled:opacity-50">Done</button>
        </div>
      </div>
    </div>
  );
}

function CollectionPage({ coll, videos, modelNames, collections, onPlay, onChanged, onBack }:
  { coll: Collection; videos: Video[]; modelNames: string[]; collections: Collection[];
    onPlay: (v: Video) => void; onChanged: () => void; onBack: () => void }) {
  const [renaming, setRenaming] = useState(false);
  const [name, setName] = useState(coll.name);

  const saveName = async () => {
    const n = name.trim(); setRenaming(false);
    if (n && n !== coll.name) { await RenameCollection(coll.id, n); onChanged(); }
  };
  const toggleHidden = async () => { await SetCollectionHidden(coll.id, !coll.hidden); onChanged(); };
  const toggleLocked = async () => { await SetCollectionLocked(coll.id, !coll.locked); onChanged(); };
  const del = async () => {
    if (!window.confirm(`Delete the collection "${coll.name}"? Your videos stay in the library.`)) return;
    await DeleteCollection(coll.id); onChanged(); onBack();
  };

  return (
    <>
      <div className="flex items-center gap-3 mb-5 flex-wrap">
        <button onClick={onBack} className="text-muted hover:text-fg text-sm">← Library</button>
        {renaming
          ? <input value={name} autoFocus onChange={(e) => setName(e.target.value)} onBlur={saveName}
              onKeyDown={(e) => { if (e.key === "Enter") saveName(); if (e.key === "Escape") setRenaming(false); }}
              className="text-xl font-semibold bg-panel2 border border-edge rounded px-2 py-1 outline-none focus:border-accent" />
          : <h1 onClick={() => { setName(coll.name); setRenaming(true); }} className="text-xl font-bold cursor-text flex items-center gap-2">
              <Icon name={coll.hidden ? "hidden" : "folder"} className="w-5 h-5 text-accent" />{coll.name} <span className="text-muted text-sm">✎</span>
            </h1>}
        <span className="text-muted text-sm">{videos.length} video{videos.length === 1 ? "" : "s"}</span>
        <div className="ml-auto flex items-center gap-2 text-xs">
          <button onClick={toggleHidden} className={`px-3 py-1.5 rounded-lg font-medium ${coll.hidden ? "bg-edge text-fg" : "bg-edge text-muted hover:text-fg"}`}>
            {coll.hidden ? "🙈 Hidden" : "👁 Visible"}
          </button>
          <button onClick={toggleLocked} className={`px-3 py-1.5 rounded-lg font-medium ${coll.locked ? "bg-edge text-fg" : "bg-edge text-muted hover:text-fg"}`}>
            {coll.locked ? "🔒 Locked" : "🔓 Unlocked"}
          </button>
          <button onClick={del} className="px-3 py-1.5 rounded-lg font-medium bg-edge text-muted hover:text-rose-400">Delete</button>
        </div>
      </div>
      {coll.hidden && <p className="text-xs text-muted mb-4 -mt-2">This collection is hidden — its videos stay out of the library, search, and model grids.</p>}
      <VideoArea videos={videos} modelNames={modelNames} collections={collections} collectionId={coll.id} onPlay={onPlay} onChanged={onChanged} />
    </>
  );
}

/* ---------------- Model page (photos + videos) ---------------- */

function ModelPage({ name, version, modelNames, onPlay, onChanged }:
  { name: string; version: number; modelNames: string[]; onPlay: (v: Video, list?: Video[]) => void; onChanged: () => void }) {
  const [videos, setVideos] = useState<Video[]>([]);
  const [photos, setPhotos] = useState<Photo[]>([]);
  const [lightbox, setLightbox] = useState<number | null>(null);
  const [info, setInfo] = useState<ModelInfo | null>(null);
  const [editing, setEditing] = useState(false);
  const [avatarOpen, setAvatarOpen] = useState(false);
  const [loading, setLoading] = useState(true);

  const load = useCallback(() => {
    setLoading(true);
    VideosByModel(name).then((v) => { setVideos(v || []); setLoading(false); });
    PhotosByModel(name).then((p) => setPhotos(p || []));
    if (name) GetModelInfo(name).then(setInfo); else setInfo(null);
  }, [name]);
  useEffect(() => { load(); }, [load, version]);
  useEffect(() => { const off = EventsOn("import", (s: any) => { if (s.finished) load(); }); return () => { off(); }; }, [load]);
  const changed = () => { load(); onChanged(); };

  const avatar = info?.cover || videos[0]?.thumbnail || "";
  const totalSecs = videos.reduce((s, v) => s + (v.duration || 0), 0);
  const totalBytes = videos.reduce((s, v) => s + (v.filesize || 0), 0);
  const sites = Array.from(new Set(videos.map((v) => v.site)));

  return (
    <>
      {name && (
        <section className="relative overflow-hidden rounded-2xl border border-edge mb-6">
          {avatar && <img src={mediaURL(avatar)} aria-hidden className="absolute inset-0 w-full h-full object-cover opacity-25 blur-2xl scale-125" />}
          <div className="absolute inset-0 bg-gradient-to-t from-ink via-ink/85 to-ink/50" />
          <div className="relative p-5 md:p-8 flex flex-col md:flex-row items-center md:items-end gap-5 md:gap-7">
            <button onClick={() => setAvatarOpen(true)} title="Change avatar"
              className="avatar relative w-28 h-28 md:w-40 md:h-40 shrink-0 group">
              {avatar
                ? <img src={mediaURL(avatar)} className="w-full h-full object-cover" />
                : <div className="w-full h-full grid place-items-center text-4xl text-muted">{(name[0] || "?").toUpperCase()}</div>}
              <span className="absolute inset-0 bg-black/45 opacity-0 group-hover:opacity-100 grid place-items-center text-white text-xs font-semibold transition">✎ Edit</span>
            </button>
            <div className="flex-1 min-w-0 text-center md:text-left">
              <h1 className="text-2xl md:text-4xl font-extrabold tracking-tight">{name}</h1>
              <div className="text-sm text-muted mt-1.5 flex flex-wrap gap-x-2 gap-y-1 justify-center md:justify-start items-center">
                <span>{videos.length} video{videos.length === 1 ? "" : "s"}</span>
                {totalSecs ? <><span>·</span><span>{fmtTotal(totalSecs)}</span></> : null}
                {totalBytes ? <><span>·</span><span>{fmtSize(totalBytes)}</span></> : null}
                {sites.length > 0 && <><span>·</span><span className="inline-flex gap-2 items-center">{sites.map((sname) => <SourceBadge key={sname} site={sname} />)}</span></>}
              </div>
              {info?.bio && <p className="text-sm text-fg/80 whitespace-pre-wrap mt-3 max-w-2xl">{info.bio}</p>}
              {info && info.links && info.links.length > 0 && (
                <div className="flex flex-wrap gap-2 mt-3 justify-center md:justify-start">
                  {info.links.map((l, i) => (
                    <button key={i} onClick={() => BrowserOpenURL(l.url)} style={{ background: "var(--ac)", color: "var(--ac-ink)" }}
                      className="text-xs font-semibold px-3 py-1.5 rounded-full">{l.label} ↗</button>
                  ))}
                </div>
              )}
              <div className="flex flex-wrap gap-2 mt-4 justify-center md:justify-start">
                {videos.length > 0 && <button onClick={() => onPlay(videos[0], videos)} className="glow-btn px-5 py-2 text-sm flex items-center gap-2">▶ Play all</button>}
                {videos.length > 0 && <button onClick={() => onPlay(videos[Math.floor(Math.random() * videos.length)], videos)} className="px-4 py-2 text-sm font-semibold rounded-xl bg-panel2 hover:bg-edge text-fg border border-edge flex items-center gap-1.5">⤮ Shuffle</button>}
                <button onClick={() => setAvatarOpen(true)} className="px-4 py-2 text-sm font-medium rounded-xl bg-panel2 hover:bg-edge text-fg border border-edge">Edit avatar</button>
                <button onClick={() => setEditing(true)} className="px-4 py-2 text-sm font-medium rounded-xl bg-panel2 hover:bg-edge text-fg border border-edge">Edit profile</button>
              </div>
            </div>
          </div>
        </section>
      )}

      <div className="flex items-center gap-3 mb-3">
        <h2 className="text-sm font-semibold text-muted">Photos{photos.length ? ` (${photos.length})` : ""}</h2>
        <button onClick={() => ImportPhotosDialog(name)} className="text-xs font-medium px-3 py-1.5 rounded-lg bg-panel2 hover:bg-edge text-fg border border-edge">Add photos…</button>
      </div>
      {photos.length > 0 && (
        <div className="flex gap-2 overflow-x-auto pb-3 mb-6">
          {photos.map((p, i) => (
            <img key={p.id} src={mediaURL(p.filepath)} loading="lazy" onClick={() => setLightbox(i)}
              className="h-44 rounded-lg object-cover cursor-pointer hover:opacity-80 shrink-0" />
          ))}
        </div>
      )}
      {loading ? <CardGridSkeleton /> : <VideoArea videos={videos} modelNames={modelNames} onPlay={onPlay} onChanged={changed} />}
      {lightbox !== null && (
        <Lightbox photos={photos} index={lightbox} onIndex={setLightbox} onClose={() => setLightbox(null)}
          onSetCover={name ? (p) => { SetModelCover(name, p.filepath).then(changed); setLightbox(null); } : undefined} />
      )}
      {editing && info && <ProfileEditor info={info} onClose={() => setEditing(false)} onSaved={() => { setEditing(false); load(); onChanged(); }} />}
      {avatarOpen && <AvatarEditor name={name} videos={videos} onClose={() => setAvatarOpen(false)} onSaved={changed} />}
    </>
  );
}

// AvatarEditor sets a model's profile picture: upload a file, paste an image URL,
// or pick a frame from one of their videos.
function AvatarEditor({ name, videos, onClose, onSaved }:
  { name: string; videos: Video[]; onClose: () => void; onSaved: () => void }) {
  const [url, setUrl] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const fileRef = useRef<HTMLInputElement>(null);
  const done = () => { setBusy(false); onSaved(); onClose(); };
  const fail = (m: string) => { setBusy(false); setErr(m); };
  const fromUrl = async () => { if (!url.trim()) return; setBusy(true); setErr(""); try { await SetAvatarFromURL(name, url.trim()); done(); } catch { fail("Couldn't fetch that image URL."); } };
  const fromPornhub = async () => { setBusy(true); setErr(""); try { const r = await FetchAvatar(name); if (r.set) done(); else fail("No Pornhub profile picture found — try upload, a URL, or a video frame."); } catch { fail("Couldn't reach Pornhub."); } };
  const fromFile = async (f?: File) => { if (!f) return; setBusy(true); setErr(""); try { await UploadAvatar(name, f); done(); } catch { fail("Upload failed."); } };
  const fromVideo = async (v: Video) => { if (!v.thumbnail) return; setBusy(true); setErr(""); try { await SetModelCover(name, v.thumbnail); done(); } catch { fail("Couldn't set that frame."); } };
  const withThumb = videos.filter((v) => v.thumbnail);

  return (
    <div className="fixed inset-0 bg-black/65 backdrop-blur-sm z-50 grid place-items-center p-4" onClick={onClose}>
      <div className="bg-panel border border-edge rounded-xl p-5 w-[92vw] max-w-[34rem] max-h-[85vh] overflow-y-auto pop" onClick={(e) => e.stopPropagation()}>
        <div className="font-semibold mb-3">Set {name}'s avatar</div>
        <button onClick={fromPornhub} disabled={busy} style={{ background: "var(--ac)", color: "var(--ac-ink)" }}
          className="w-full mb-3 py-2 rounded-lg text-sm font-semibold disabled:opacity-50 flex items-center justify-center gap-2">✨ Fetch from Pornhub</button>
        <div className="flex flex-col sm:flex-row gap-2 mb-3">
          <button onClick={() => fileRef.current?.click()} disabled={busy}
            className="text-sm font-medium px-4 py-2 rounded-lg bg-panel2 hover:bg-edge text-fg border border-edge disabled:opacity-50">⬆ Upload image…</button>
          <input ref={fileRef} type="file" accept="image/*" className="hidden" onChange={(e) => fromFile(e.target.files?.[0])} />
          <div className="flex gap-2 flex-1">
            <input value={url} onChange={(e) => setUrl(e.target.value)} onKeyDown={(e) => { if (e.key === "Enter") fromUrl(); }}
              placeholder="…or paste an image URL" className="flex-1 bg-panel2 border border-edge rounded-lg px-3 py-2 text-sm outline-none focus:border-accent" />
            <button onClick={fromUrl} disabled={busy || !url.trim()} style={{ background: "var(--ac)", color: "var(--ac-ink)" }}
              className="text-sm font-semibold px-4 rounded-lg disabled:opacity-50">Set</button>
          </div>
        </div>
        {err && <div className="text-xs text-rose-400 mb-2">{err}</div>}
        {withThumb.length > 0 && (
          <>
            <div className="text-xs text-muted mb-2 mt-1">…or pick a frame from a video</div>
            <div className="grid grid-cols-3 sm:grid-cols-4 gap-2">
              {withThumb.slice(0, 24).map((v) => (
                <button key={v.site + v.id} onClick={() => fromVideo(v)} disabled={busy}
                  className="aspect-square rounded-lg overflow-hidden border border-edge hover:border-accent transition disabled:opacity-50">
                  <img src={mediaURL(v.thumbnail)} loading="lazy" className="w-full h-full object-cover" />
                </button>
              ))}
            </div>
          </>
        )}
        <div className="flex justify-end mt-4">
          <button onClick={onClose} className="text-sm text-muted hover:text-fg px-3 py-2">Close</button>
        </div>
      </div>
    </div>
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
    <div className="fixed inset-0 bg-black/65 backdrop-blur-sm z-50 grid place-items-center p-4" onClick={onClose}>
      <div className="bg-panel border border-edge rounded-xl p-5 w-[92vw] max-w-[28rem] max-h-[85vh] overflow-y-auto" onClick={(e) => e.stopPropagation()}>
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
        <button onClick={() => setLinks((ls) => [...ls, { label: "", url: "" }])} className="text-xs text-muted hover:text-fg mb-4">+ add link</button>
        <div className="flex justify-end gap-2">
          <button onClick={onClose} className="text-sm text-muted hover:text-fg px-3 py-2">Cancel</button>
          <button onClick={save} style={{ background: "var(--ac)", color: "var(--ac-ink)" }} className="text-sm font-semibold px-4 py-2 rounded-lg">Save</button>
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
          className="absolute top-4 left-4 text-xs font-semibold px-3 py-1.5 rounded-lg" style={{ background: "var(--ac)", color: "var(--ac-ink)" }}>
          Set as cover
        </button>
      )}
      <div className="absolute bottom-4 text-muted text-xs">{index + 1} / {n}</div>
    </div>
  );
}

/* ---------------- Video area (grid + bulk select) ---------------- */

function VideoArea({ videos, groups, modelNames, collections, collectionId, onPlay, onChanged }:
  { videos: Video[]; groups?: { title: string; videos: Video[] }[]; modelNames: string[];
    collections?: Collection[]; collectionId?: number;
    onPlay: (v: Video, list?: Video[]) => void; onChanged: () => void }) {
  const [selectMode, setSelectMode] = useState(false);
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [moving, setMoving] = useState(false);
  const [addingColl, setAddingColl] = useState(false);
  const key = (v: Video) => v.site + "/" + v.id;

  const toggle = (v: Video) => setPicked((p) => { const n = new Set(p); const k = key(v); n.has(k) ? n.delete(k) : n.add(k); return n; });
  const exit = () => { setSelectMode(false); setPicked(new Set()); };
  const pickedVideos = videos.filter((v) => picked.has(key(v)));
  const removeFromColl = async () => {
    if (collectionId == null) return;
    await Promise.all(pickedVideos.map((v) => RemoveFromCollection(collectionId, v.site, v.id)));
    exit(); onChanged();
  };

  if (!videos?.length) return <Empty icon="🍧">Nothing in here yet.</Empty>;
  return (
    <>
      <div className="flex items-center gap-3 mb-3 text-sm">
        {!selectMode
          ? <>
              <button onClick={() => onPlay(videos[0], videos)} className="glow-btn px-4 py-1.5 text-xs flex items-center gap-1.5">▶ Play all</button>
              <button onClick={() => { const v = videos[Math.floor(Math.random() * videos.length)]; onPlay(v, videos); }} className="px-3 py-1.5 text-xs font-medium rounded-lg bg-panel2 hover:bg-edge text-fg border border-edge flex items-center gap-1.5">⤮ Shuffle</button>
              <button onClick={() => setSelectMode(true)} className="ml-auto text-muted hover:text-fg">Select</button>
            </>
          : <>
              <span className="text-muted">{picked.size} selected</span>
              <button onClick={() => setMoving(true)} disabled={!picked.size} style={{ background: "var(--ac)", color: "var(--ac-ink)" }}
                className="font-semibold px-3 py-1.5 rounded-lg text-xs disabled:opacity-40">Move to person…</button>
              {collections && (
                <button onClick={() => setAddingColl(true)} disabled={!picked.size}
                  className="font-medium px-3 py-1.5 rounded-lg text-xs bg-panel2 hover:bg-edge text-fg border border-edge disabled:opacity-40">Add to collection…</button>
              )}
              {collectionId != null && (
                <button onClick={removeFromColl} disabled={!picked.size}
                  className="font-medium px-3 py-1.5 rounded-lg text-xs bg-edge text-muted hover:text-rose-400 disabled:opacity-40">Remove from collection</button>
              )}
              <button onClick={exit} className="text-muted hover:text-fg text-xs">Cancel</button>
            </>}
      </div>
      {(groups && groups.length ? groups : [{ title: "", videos }]).map((g, gi) => (
        <section key={g.title || gi}>
          {g.title && (
            <h3 className="text-[13px] font-bold text-muted mt-6 mb-2.5 first:mt-0 flex items-baseline gap-2">
              {g.title} <span className="text-[11px] font-semibold text-muted/60">{g.videos.length}</span>
            </h3>
          )}
          <div className="grid gap-3 md:gap-4" style={{ gridTemplateColumns: "repeat(auto-fill,minmax(clamp(150px,44vw,290px),1fr))" }}>
            {g.videos.map((v) => (
              <VideoCard key={key(v)} v={v} selectMode={selectMode} selected={picked.has(key(v))}
                onClick={() => (selectMode ? toggle(v) : onPlay(v, videos))} />
            ))}
          </div>
        </section>
      ))}
      {moving && (
        <ModelsEditor refs={pickedVideos.map((v) => ({ site: v.site, id: v.id }))} modelNames={modelNames}
          onClose={() => setMoving(false)} onDone={() => { setMoving(false); exit(); onChanged(); }} />
      )}
      {addingColl && collections && (
        <AddToCollectionModal refs={pickedVideos.map((v) => ({ site: v.site, id: v.id }))} collections={collections}
          onClose={() => setAddingColl(false)} onChanged={() => { exit(); onChanged(); }} />
      )}
    </>
  );
}

function VideoCard({ v, onClick, selectMode, selected }:
  { v: Video; onClick: () => void; selectMode?: boolean; selected?: boolean }) {
  return (
    <div onClick={onClick} className="tile group"
      style={selected ? { boxShadow: "0 0 0 2px var(--ac), 0 14px 44px rgba(0,0,0,.65)" } : undefined}>
      <PreviewMedia v={v} ratio="aspect-video" />
      <div className="overlay" />
      <div className="absolute top-2 left-2 flex items-center gap-1.5">
        <span className="bg-black/50 text-white backdrop-blur-sm rounded-lg px-1.5 py-0.5 shadow-sm"><SourceBadge site={v.site} /></span>
        {isNew(v.added) && <span className="swirl-chip text-[10px] font-extrabold px-2 py-0.5 rounded-full">NEW</span>}
      </div>
      {selectMode && (
        <span className={`absolute top-2 right-2 w-6 h-6 grid place-items-center rounded-full text-sm font-bold ${selected ? "" : "border-2 border-white/70 bg-black/30"}`}
          style={selected ? { background: "var(--ac)", color: "var(--ac-ink)" } : undefined}>{selected ? "✓" : ""}</span>
      )}
      {!selectMode && v.duration ? <span className="absolute top-2 right-2 bg-black/75 text-white text-[11px] px-1.5 py-0.5 rounded">{fmtDur(v.duration)}</span> : null}
      <div className="absolute bottom-0 left-0 right-0 p-3">
        <div className="text-sm font-semibold line-clamp-1 leading-snug text-white cap">{v.favorite ? <span className="text-rose-300">❤ </span> : null}{v.title || v.uploader}</div>
        <div className="text-xs text-white/85 mt-1 truncate cap">{(v.models && v.models.length ? v.models.join(", ") : UNASSIGNED)}{v.height ? ` · ${v.height}p` : ""}{v.filesize ? ` · ${fmtSize(v.filesize)}` : ""}</div>
      </div>
      {v.position && v.duration && v.position < v.duration * 0.95
        ? <div className="progress"><i style={{ width: `${Math.round((v.position / v.duration) * 100)}%` }} /></div>
        : null}
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
    <div className="fixed inset-0 bg-black/65 backdrop-blur-sm z-50 grid place-items-center p-4" onClick={onClose}>
      <div className="bg-panel border border-edge rounded-xl p-5 w-[92vw] max-w-[26rem] pop" onClick={(e) => e.stopPropagation()}>
        <div className="font-semibold mb-1">{bulk ? `Set people for ${refs.length} videos` : "People"}</div>
        <p className="text-xs text-muted mb-3">Add one or more people. Type a new name to create it. No one = Unsorted.</p>

        <div className="flex flex-wrap gap-2 mb-3 min-h-[2rem]">
          {models.length === 0 && <span className="text-xs text-muted py-1">No one yet</span>}
          {models.map((m) => (
            <span key={m} className="flex items-center gap-1 bg-panel2 border border-edge rounded-full px-3 py-1 text-sm">
              {m} <button onClick={() => setModels(models.filter((x) => x !== m))} className="text-muted hover:text-rose-400">×</button>
            </span>
          ))}
        </div>

        <div className="flex gap-2">
          <input list="me-models" value={input} autoFocus onChange={(e) => setInput(e.target.value)}
            onKeyDown={(e) => { if (e.key === "Enter") add(); }} placeholder="Add a person…"
            className="flex-1 bg-panel2 border border-edge rounded-lg px-3 py-2 text-sm outline-none focus:border-accent" />
          <button onClick={add} className="text-sm font-medium px-3 py-2 rounded-lg bg-panel2 hover:bg-edge text-fg border border-edge">Add</button>
          <datalist id="me-models">{modelNames.map((n) => <option key={n} value={n} />)}</datalist>
        </div>

        <div className="flex justify-end gap-2 mt-4">
          <button onClick={onClose} className="text-sm text-muted hover:text-fg px-3 py-2">Cancel</button>
          <button onClick={save} disabled={busy} style={{ background: "var(--ac)", color: "var(--ac-ink)" }}
            className="text-sm font-semibold px-4 py-2 rounded-lg disabled:opacity-50">Save</button>
        </div>
      </div>
    </div>
  );
}

/* ---------------- Watch page (YouTube-style) ---------------- */

function WatchPage({ video, queue, allLabels, modelNames, collections, onClose, onPlay, onOpenModel, onChanged }:
  { video: Video; queue: Video[]; allLabels: string[]; modelNames: string[]; collections: Collection[]; onClose: () => void; onPlay: (v: Video, list?: Video[]) => void; onOpenModel: (name: string) => void; onChanged: () => void }) {
  const [related, setRelated] = useState<Video[]>([]);
  const [editing, setEditing] = useState(false);
  const [tv, setTv] = useState(video.title || "");
  const [fav, setFav] = useState(!!video.favorite);
  const [labels, setLabels] = useState<string[]>(video.labels || []);
  const [models, setModelsState] = useState<string[]>(video.models || []);
  const [newLabel, setNewLabel] = useState("");
  const [moving, setMoving] = useState(false);
  const [addingColl, setAddingColl] = useState(false);
  const [copied, setCopied] = useState(false);
  const [vidErr, setVidErr] = useState(false);
  const topRef = useRef<HTMLDivElement>(null);
  const primary = models[0] || "";

  // Lean-back playback: resume point, autoplay-next, and the up-next queue.
  const vidRef = useRef<HTMLVideoElement>(null);
  const lastSave = useRef(0);
  const [autoplay, setAutoplay] = useState(() => localStorage.getItem("autoplayNext") !== "0");
  const idx = queue.findIndex((x) => x.site === video.site && x.id === video.id);
  const next = idx >= 0 && idx + 1 < queue.length ? queue[idx + 1] : null;

  const savePos = (force = false) => {
    const el = vidRef.current;
    if (!el || !el.duration) return;
    const pos = el.currentTime;
    if (force || Math.abs(pos - lastSave.current) >= 5) {
      lastSave.current = pos;
      SetPosition(video.site, video.id, pos, el.duration);
    }
  };
  const onLoaded = () => {
    const el = vidRef.current;
    const p = video.position || 0;
    if (el && p > 15 && el.duration && p < el.duration - 10) el.currentTime = p;
  };
  const onEnded = () => {
    const dur = vidRef.current?.duration || 0;
    SetPosition(video.site, video.id, dur, dur); // >=95% clears the resume point
    if (autoplay && next) onPlay(next, queue);
  };
  // Persist progress periodically and on leave, so Continue Watching is accurate.
  useEffect(() => {
    const iv = window.setInterval(() => savePos(), 5000);
    return () => { window.clearInterval(iv); savePos(true); };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [video.site, video.id]);

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
    <div ref={topRef} className="fixed inset-0 z-30 md:left-56 bg-ink overflow-y-auto rise">
      <div className="sticky top-0 z-10 glass border-b border-edge/70" style={{ paddingTop: "env(safe-area-inset-top)" }}>
        <div className="px-4 md:px-5 py-2.5">
          <button onClick={onClose} className="text-muted hover:text-fg text-sm font-semibold px-2 py-1.5 -ml-2 rounded-full active:bg-panel2">← Back</button>
        </div>
      </div>
      <div className="max-w-5xl mx-auto p-4 md:p-6 pb-28 md:pb-6">
        <div className="relative bg-black -mx-4 -mt-4 md:mx-0 md:mt-0 md:rounded-blob overflow-hidden shadow-lg shadow-black/40">
          <video ref={vidRef} src={videoURL(video.filepath)} controls autoPlay playsInline preload="metadata"
            poster={video.thumbnail ? mediaURL(video.thumbnail) : undefined}
            onLoadedMetadata={onLoaded} onTimeUpdate={() => savePos()} onPause={() => savePos(true)} onEnded={onEnded}
            onError={() => setVidErr(true)}
            className="w-full max-h-[68vh] min-h-[220px] object-contain bg-black" />
          {vidErr && (
            <div className="absolute inset-0 grid place-items-center bg-black/85 text-center p-6 pointer-events-none">
              <div>
                <div className="text-3xl mb-2">🫠</div>
                <div className="text-white font-semibold mb-1">This video couldn't be played here</div>
                <p className="text-white/60 text-xs max-w-xs mx-auto">
                  The device may not support this file. Try "Fix videos for mobile" in Settings, or play it on the desktop app.
                </p>
              </div>
            </div>
          )}
        </div>

        {queue.length > 1 && (
          <div className="flex items-center gap-3 mt-3 text-sm">
            <label className="flex items-center gap-2 cursor-pointer select-none">
              <input type="checkbox" checked={autoplay} className="w-4 h-4 accent-[color:var(--ac)]"
                onChange={(e) => { setAutoplay(e.target.checked); localStorage.setItem("autoplayNext", e.target.checked ? "1" : "0"); }} />
              <span className="text-muted">Autoplay next</span>
            </label>
            {next && (
              <button onClick={() => onPlay(next, queue)} className="ml-auto text-fg hover:text-accent font-medium truncate max-w-[60%]">
                Next: <span className="text-muted">{next.title || next.uploader}</span> ▶
              </button>
            )}
          </div>
        )}

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
                  <button key={m} onClick={() => onOpenModel(m)} className="font-medium text-fg/90 hover:text-fg">{m}</button>
                ))}
            <button onClick={() => setMoving(true)} title="Edit models" className="text-muted hover:text-fg">✎</button>
          </div>
          <span className="text-muted">{[label(video.site), video.height ? `${video.height}p` : "", fmtSize(video.filesize), video.upload_date].filter(Boolean).join("  ·  ")}</span>
          <div className="md:ml-auto w-full md:w-auto flex items-center flex-wrap gap-2">
            <button onClick={toggleFav} className={`flex items-center gap-1.5 px-3.5 py-2 rounded-full text-sm font-semibold transition ${fav ? "bg-rose-500 text-white" : "bg-panel2 text-fg hover:bg-edge border border-edge"}`}>
              {fav ? "❤ Liked" : "♡ Like"}
            </button>
            <button onClick={() => setAddingColl(true)} className="px-3.5 py-2 rounded-full text-sm font-medium bg-panel2 text-fg hover:bg-edge border border-edge">+ Collection</button>
            {video.webpage_url && <button onClick={copy} className="px-3.5 py-2 rounded-full text-sm font-medium bg-panel2 text-fg hover:bg-edge border border-edge">{copied ? "Copied ✓" : "Copy link"}</button>}
            {video.webpage_url && <button onClick={() => BrowserOpenURL(video.webpage_url)} className="px-3.5 py-2 rounded-full text-sm font-medium bg-panel2 text-fg hover:bg-edge border border-edge">Source ↗</button>}
            {isDesktopApp && <button onClick={() => OpenFolder(video.filepath)} className="px-3.5 py-2 rounded-full text-sm font-medium bg-panel2 text-fg hover:bg-edge border border-edge">Show file</button>}
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-2 mt-4">
          <Icon name="tag" className="w-4 h-4 text-muted" />
          {labels.map((l) => (
            <span key={l} className="flex items-center gap-1 bg-panel2 border border-edge rounded-full px-3 py-1 text-xs">
              {l} <button onClick={() => commitLabels(labels.filter((x) => x !== l))} className="text-muted hover:text-rose-400">×</button>
            </span>
          ))}
          <input list="all-labels" value={newLabel} onChange={(e) => setNewLabel(e.target.value)}
            onKeyDown={(e) => { if (e.key === "Enter") addLabel(); }} placeholder="+ add tag"
            className="bg-panel border border-edge rounded-full px-3 py-1 text-xs w-36 outline-none focus:border-accent" />
          <datalist id="all-labels">{allLabels.map((l) => <option key={l} value={l} />)}</datalist>
        </div>

        {next && (
          <div className="mt-8">
            <h2 className="font-semibold mb-3">Up next</h2>
            <div className="grid gap-3 md:gap-4" style={{ gridTemplateColumns: "repeat(auto-fill,minmax(clamp(150px,44vw,260px),1fr))" }}>
              {queue.slice(idx + 1, idx + 13).map((v) => <VideoCard key={v.site + "/" + v.id} v={v} onClick={() => onPlay(v, queue)} />)}
            </div>
          </div>
        )}

        {related.length > 0 && (
          <div className="mt-8">
            <h2 className="font-semibold mb-3">More from {primary || "this collection"}</h2>
            <div className="grid gap-3 md:gap-4" style={{ gridTemplateColumns: "repeat(auto-fill,minmax(clamp(150px,44vw,260px),1fr))" }}>
              {related.map((v) => <VideoCard key={v.site + "/" + v.id} v={v} onClick={() => onPlay(v, related)} />)}
            </div>
          </div>
        )}
      </div>
      {moving && (
        <ModelsEditor refs={[{ site: video.site, id: video.id }]} modelNames={modelNames} initial={models}
          onClose={() => setMoving(false)}
          onDone={(m) => { setMoving(false); setModelsState(m); video.models = m; loadRelated(m[0] || ""); onChanged(); }} />
      )}
      {addingColl && (
        <AddToCollectionModal refs={[{ site: video.site, id: video.id }]} collections={collections}
          onClose={() => setAddingColl(false)} onChanged={onChanged} />
      )}
    </div>
  );
}

/* ---------------- Sync ---------------- */

function BrowseSync({ onEnqueued }: { onEnqueued: () => void }) {
  const [url, setUrl] = useState("");
  const [loadedURL, setLoadedURL] = useState("");
  const [items, setItems] = useState<downloader.RemoteItem[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [status, setStatus] = useState({ x: false, pornhub: false });
  const [fav, setFav] = useState(localStorage.phUser || "");
  const [lists, setLists] = useState<SyncSummary[]>([]);
  const [adding, setAdding] = useState(false);

  const loadLists = () => SyncedLists().then((l) => setLists(l || []));
  useEffect(() => { CookieStatus().then(setStatus); loadLists(); }, []);

  // refresh=false uses the cached list (instant); refresh=true re-fetches from the site.
  const loadURL = async (u: string, refresh = false) => {
    u = u.trim(); if (!u) return;
    setLoadedURL(u); setLoading(true); setItems(null); setPicked(new Set());
    try { setItems((await Enumerate(u, refresh)) || []); } catch { setItems([]); } finally { setLoading(false); }
    loadLists();
  };
  const loadFavorites = () => {
    const user = fav.trim(); if (!user) return;
    localStorage.phUser = user;
    loadURL(`https://www.pornhub.com/users/${user}/videos/favorites`);
  };
  const remove = async (u: string) => { await RemoveSync(u); loadLists(); };
  const back = () => { setItems(null); setLoadedURL(""); loadLists(); };
  const toggle = (u: string) => setPicked((p) => { const n = new Set(p); n.has(u) ? n.delete(u) : n.add(u); return n; });
  const newItems = (items || []).filter((i) => !i.owned);
  const ownedCount = (items || []).length - newItems.length;
  const download = async () => { await EnqueueMany([...picked]); setPicked(new Set()); onEnqueued(); };
  const openTitle = lists.find((l) => l.url === loadedURL)?.title || syncLabelOf(loadedURL);

  const showList = items !== null || loading;

  return (
    <div className="p-4 md:p-6 max-w-4xl">
      <div className="flex items-center gap-3 mb-1">
        <h1 className="text-xl font-bold">Following</h1>
        {!showList && <button onClick={() => setAdding((a) => !a)} className="ml-auto text-sm font-semibold px-4 py-1.5 glow-btn">{adding ? "Close" : "+ Follow"}</button>}
      </div>
      <p className="text-sm text-muted mb-4">People, channels, and lists you keep an eye on — open one to see what's new since last time.</p>

      {/* Add-a-sync panel */}
      {!showList && adding && (
        <div className="bg-panel border border-edge rounded-xl p-4 mb-6 pop">
          <div className="text-sm font-semibold mb-2">Paste a Pornhub link</div>
          <div className="flex gap-2">
            <input value={url} onChange={(e) => setUrl(e.target.value)} onKeyDown={(e) => { if (e.key === "Enter") loadURL(url); }} autoFocus
              placeholder="https://www.pornhub.com/model/NAME/videos"
              className="flex-1 bg-panel2 border border-edge rounded-lg px-4 py-2.5 text-sm outline-none focus:border-accent" />
            <button onClick={() => loadURL(url)} disabled={loading} className="glow-btn px-6 rounded-lg disabled:opacity-50">Load</button>
          </div>
          <div className="flex items-center flex-wrap gap-2 mt-3 text-sm">
            <span className="text-muted">❤ Favorites:</span>
            <input value={fav} onChange={(e) => setFav(e.target.value)} onKeyDown={(e) => { if (e.key === "Enter") loadFavorites(); }}
              placeholder="your pornhub username" className="w-48 bg-panel2 border border-edge rounded-lg px-3 py-2 outline-none focus:border-accent" />
            <button onClick={loadFavorites} disabled={!status.pornhub || !fav.trim()}
              className="text-xs font-semibold px-3 py-2 rounded-lg bg-panel2 hover:bg-edge text-fg border border-edge disabled:opacity-40">Sync favorites</button>
            {!status.pornhub && <span className="text-xs text-amber-400">Connect Pornhub in Settings first</span>}
          </div>
        </div>
      )}

      {/* Saved syncs */}
      {!showList && (
        lists.length === 0
          ? <Empty icon="✨" action={{ label: "+ Follow someone", onClick: () => setAdding(true) }}>You're not following anything yet. Add a person, channel, or your favorites and trove will keep track of what's new.</Empty>
          : (
            <div className="grid gap-3" style={{ gridTemplateColumns: "repeat(auto-fill,minmax(240px,1fr))" }}>
              {lists.map((l) => (
                <SyncCard key={l.url} l={l} onOpen={() => loadURL(l.url)} onRefresh={() => loadURL(l.url, true)} onRemove={() => remove(l.url)} />
              ))}
            </div>
          )
      )}

      {/* Open a list */}
      {showList && (
        <>
          <div className="flex items-center gap-3 mb-4">
            <button onClick={back} className="text-muted hover:text-fg text-sm">← Following</button>
            <h2 className="text-lg font-bold truncate">{openTitle}</h2>
          </div>
          {loading && <CardGridSkeleton count={8} ratio="h-12" />}
          {items && items.length > 0 && (
            <>
              <div className="flex items-center flex-wrap gap-3 mb-3 text-sm sticky top-0 bg-ink/95 backdrop-blur py-2 z-10">
                <span className="font-semibold">{items.length} videos</span>
                <span className="text-muted">· {ownedCount} owned · {newItems.length} new · {picked.size} selected</span>
                <button onClick={() => loadURL(loadedURL, true)} disabled={loading} className="text-xs text-muted hover:text-fg">↻ Refresh</button>
                <button onClick={() => setPicked(new Set(newItems.map((i) => i.url)))} className="ml-auto text-xs text-muted hover:text-fg">Select all new</button>
                <button onClick={download} disabled={!picked.size} className="glow-btn text-xs px-4 py-2 rounded-lg disabled:opacity-40">Download {picked.size || ""}</button>
              </div>
              <div className="space-y-1">
                {items.map((it) => (
                  <label key={it.url} className={`flex items-center gap-3 px-3 py-2 rounded-lg ${it.owned ? "opacity-50" : "hover:bg-panel2 cursor-pointer"}`}>
                    {it.owned
                      ? <span className="text-xs text-emerald-400 w-16 shrink-0">in library</span>
                      : <input type="checkbox" checked={picked.has(it.url)} onChange={() => toggle(it.url)} className="w-4 h-4 accent-[color:var(--ac)] shrink-0" />}
                    <span className="text-sm truncate">{it.title || it.url}</span>
                  </label>
                ))}
              </div>
            </>
          )}
          {items && items.length === 0 && !loading && <Empty>No videos found — check the URL, or connect Pornhub for private lists.</Empty>}
        </>
      )}
    </div>
  );
}

// syncLabelOf derives a readable name from a sync URL on the client (fallback
// when the saved summary isn't loaded yet).
function syncLabelOf(u: string) {
  const m = u.match(/\/(?:model|pornstar|channels?|users)\/([^/?#]+)/i);
  const base = m ? decodeURIComponent(m[1]).replace(/[-_]/g, " ") : u.replace(/^https?:\/\/(www\.)?/, "");
  return /favorit/i.test(u) ? base + " — favorites" : base;
}

function SyncCard({ l, onOpen, onRefresh, onRemove }:
  { l: SyncSummary; onOpen: () => void; onRefresh: () => void; onRemove: () => void }) {
  return (
    <div onClick={onOpen} className="group relative bg-panel border border-edge rounded-xl p-4 hover:border-accent transition cursor-pointer">
      <button onClick={(e) => { e.stopPropagation(); onRemove(); }} title="Unfollow"
        className="absolute top-2.5 right-2.5 opacity-0 group-hover:opacity-100 text-muted hover:text-rose-400 text-lg leading-none">×</button>
      <div className="flex items-center gap-3">
        <div className="w-11 h-11 rounded-xl bg-panel2 grid place-items-center text-xl shrink-0">{kindIcon(l.kind)}</div>
        <div className="min-w-0 flex-1 pr-4">
          <div className="font-semibold truncate">{l.title || l.url}</div>
          <div className="text-[11px] text-muted capitalize">{l.kind} · fetched {fmtAgo(l.fetchedAt)}</div>
        </div>
      </div>
      <div className="flex items-center gap-2 mt-3 text-xs">
        <span className="text-fg font-medium">{l.count} videos</span>
        {l.new > 0
          ? <span className="px-1.5 py-0.5 rounded-full font-semibold" style={{ background: "rgb(var(--ac-rgb) / .14)", color: "var(--ac)" }}>{l.new} new</span>
          : <span className="text-muted">all downloaded</span>}
        <button onClick={(e) => { e.stopPropagation(); onRefresh(); }} title="Re-fetch from site"
          className="ml-auto text-muted hover:text-fg px-2 py-1 rounded-lg hover:bg-panel2">↻ Refresh</button>
      </div>
    </div>
  );
}

/* ---------------- Settings ---------------- */

function SettingsPage() {
  const [root, setRoot] = useState("");
  const [changed, setChanged] = useState(false);
  const [stats, setStats] = useState<library.Stats | null>(null);
  const [rebuilding, setRebuilding] = useState(false);
  const [rebuilt, setRebuilt] = useState<number | null>(null);
  const [avBusy, setAvBusy] = useState(false);
  const [avProg, setAvProg] = useState<{ done: number; total: number; added: number; name?: string; finished?: boolean } | null>(null);
  const [accent, setAccent] = useState(localStorage.accent || "236 92 133");
  const [conn, setConn] = useState({ x: false, pornhub: false });
  const [autoNext, setAutoNext] = useState(localStorage.autoplayNext !== "0");
  const [hoverPrev, setHoverPrev] = useState(localStorage.hoverPreview !== "0");
  const [backupPath, setBackupPath] = useState("");
  const [backuping, setBackuping] = useState(false);
  const [optBusy, setOptBusy] = useState(false);
  const [optProg, setOptProg] = useState<{ done: number; total: number; fixed: number; failed: number; name?: string; finished?: boolean } | null>(null);

  useEffect(() => { MediaRootPath().then(setRoot); Stats().then(setStats); CookieStatus().then(setConn); }, []);
  useEffect(() => {
    const off = EventsOn("optimize", (s: any) => { setOptProg(s); if (s.finished) setOptBusy(false); });
    return () => off();
  }, []);
  const optimize = () => { setOptBusy(true); setOptProg(null); OptimizeStreaming(); };
  const pickAccent = (rgb: string) => { setAccent(rgb); localStorage.accent = rgb; applyAccent(rgb); };
  const connect = async () => setConn(await ConnectCookies());
  const toggleAuto = (on: boolean) => { setAutoNext(on); localStorage.autoplayNext = on ? "1" : "0"; };
  const toggleHover = (on: boolean) => { setHoverPrev(on); localStorage.hoverPreview = on ? "1" : "0"; };
  const doBackup = async () => { setBackuping(true); try { const r = await BackupCatalogue(); setBackupPath(r.path); } finally { setBackuping(false); } };
  useEffect(() => {
    const off = EventsOn("avatar", (s: any) => { setAvProg(s); if (s.finished) setAvBusy(false); });
    return () => off();
  }, []);
  const rebuild = async () => {
    setRebuilding(true); setRebuilt(null);
    try { const r = await RebuildLibrary(); setRebuilt(r.count); Stats().then(setStats); }
    finally { setRebuilding(false); }
  };
  const fetchAvatars = () => { setAvBusy(true); setAvProg(null); FetchAllAvatars(); };
  const change = async () => { const next = await ChooseMediaRoot(); if (next && next !== root) { setRoot(next); setChanged(true); } };
  const siteColor = (s: string) => (s === "PornHub" ? "#e8964e" : s === "Twitter" ? "#5da4d4" : "var(--ac)");

  return (
    <div className="p-4 md:p-6 max-w-3xl">
      <h1 className="text-xl font-semibold mb-6">Settings</h1>

      <section className="bg-panel border border-edge rounded-xl p-5 mb-6">
        <div className="text-sm font-semibold mb-3">Flavor</div>
        <div className="flex items-center gap-3">
          {ACCENTS.map((a) => (
            <button key={a.rgb} onClick={() => pickAccent(a.rgb)} title={a.name}
              className="w-10 h-10 rounded-full grid place-items-center transition"
              style={{ background: `rgb(${a.rgb})`, boxShadow: accent === a.rgb ? `0 0 0 3px var(--milk), 0 0 0 5px rgb(${a.rgb})` : "inset 0 1px 0 rgba(255,255,255,.4)" }}>
              {accent === a.rgb && <span className="text-sm font-bold text-white">✓</span>}
            </button>
          ))}
          <span className="text-xs text-muted ml-1">Pick a flavor — changes the whole app instantly.</span>
        </div>
      </section>

      <section className="bg-panel border border-edge rounded-xl p-5 mb-6">
        <div className="text-sm font-semibold mb-1">Library location</div>
        <p className="text-xs text-muted mb-3">Where your videos, thumbnails, and catalogue live. Point this at an external drive to take your vault with you.</p>
        <div className="flex items-center gap-2">
          <code className="flex-1 bg-panel2 border border-edge rounded-lg px-3 py-2 text-sm truncate">{root || "…"}</code>
          {isDesktopApp && <button onClick={change} className="text-sm font-medium px-4 py-2 rounded-lg bg-panel2 hover:bg-edge text-fg border border-edge shrink-0">Change folder…</button>}
        </div>
        {changed && (
          <div className="mt-3 flex items-center gap-3 text-sm text-amber-400">
            Saved — restart to use the new location.
            <button onClick={() => RestartApp()} style={{ background: "var(--ac)", color: "var(--ac-ink)" }} className="font-semibold px-3 py-1.5 rounded-lg">Restart now</button>
          </div>
        )}
      </section>
      <section className="bg-panel border border-edge rounded-xl p-5 mb-6">
        <div className="text-sm font-semibold mb-1">Connections</div>
        <p className="text-xs text-muted mb-3 leading-relaxed">
          Connect an account so protected / age-restricted posts and favorites work. Export with
          <b> “Get cookies.txt LOCALLY”</b> while logged in, then connect it here — saved in your vault.
        </p>
        <ConnRow label={<SourceBadge site="PornHub" />} on={conn.pornhub} onConnect={connect} />
        <ConnRow label={<SourceBadge site="Twitter" />} on={conn.x} onConnect={connect} />
      </section>

      <section className="bg-panel border border-edge rounded-xl p-5 mb-6">
        <div className="text-sm font-semibold mb-3">Playback</div>
        <ToggleRow label="Autoplay next" hint="Play the next video automatically when one ends." on={autoNext} onChange={toggleAuto} />
        <ToggleRow label="Hover preview" hint="Play a muted clip when you hover a video card (desktop only)." on={hoverPrev} onChange={toggleHover} />
      </section>

      <section className="bg-panel border border-edge rounded-xl p-5 mb-6">
        <div className="text-sm font-semibold mb-1">Fix videos for mobile</div>
        <p className="text-xs text-muted mb-3 leading-relaxed">
          Some downloads store their index at the end of the file, so phones sit on a spinner
          before anything plays. This scans the vault and rewrites those files so they start
          instantly — a lossless copy, no quality change, a few seconds per file.
        </p>
        <div className="flex items-center gap-3 flex-wrap">
          <button onClick={optimize} disabled={optBusy}
            className="text-sm font-medium px-4 py-2 rounded-lg bg-panel2 hover:bg-edge text-fg border border-edge disabled:opacity-50">
            {optBusy ? "Fixing…" : "Scan & fix now"}
          </button>
          {optProg && (optProg.finished
            ? <span className="text-sm text-emerald-400">
                {optProg.total === 0 ? "All videos already stream instantly ✓" : `Fixed ${optProg.fixed} of ${optProg.total} video${optProg.total === 1 ? "" : "s"} ✓${optProg.failed ? ` (${optProg.failed} skipped)` : ""}`}
              </span>
            : <span className="text-sm text-muted">{optProg.done}/{optProg.total} · {optProg.fixed} fixed… <span className="text-muted/60">{optProg.name || ""}</span></span>)}
        </div>
      </section>

      <section className="bg-panel border border-edge rounded-xl p-5">
        <div className="text-sm font-semibold mb-3">Storage</div>
        {!stats ? <div className="text-muted text-sm">Loading…</div> : (
          <>
            <div className="flex items-baseline gap-2 mb-4">
              <span className="text-3xl font-bold">{fmtSize(stats.totalBytes)}</span>
              <span className="text-muted text-sm">· {stats.videoCount} videos · {stats.modelCount} people</span>
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
                    <div className="h-2 bg-panel2 rounded-full overflow-hidden"><div className="h-full" style={{ width: pct + "%", background: siteColor(s.site) }} /></div>
                  </div>
                );
              })}
            </div>
          </>
        )}
      </section>

      <section className="bg-panel border border-edge rounded-xl p-5 mt-6">
        <div className="text-sm font-semibold mb-1">Backup &amp; safety</div>
        <p className="text-xs text-muted mb-3 leading-relaxed">
          Back up your catalogue (models, favorites, labels, collections, resume points) to a timestamped
          file in <code>.trove/backups</code>. Your media files aren't copied — only the database.
        </p>
        <div className="flex items-center gap-3 flex-wrap">
          <button onClick={doBackup} disabled={backuping}
            className="text-sm font-medium px-4 py-2 rounded-lg bg-panel2 hover:bg-edge text-fg border border-edge disabled:opacity-50">
            {backuping ? "Backing up…" : "Back up catalogue now"}
          </button>
          {isDesktopApp && <button onClick={() => root && OpenFolder(root)} className="text-sm font-medium px-4 py-2 rounded-lg bg-panel2 hover:bg-edge text-fg border border-edge">Reveal vault folder</button>}
          {backupPath && <span className="text-sm text-emerald-400 flex items-center gap-2">Saved ✓ {isDesktopApp && <button onClick={() => OpenFolder(backupPath)} className="text-muted hover:text-fg underline">show</button>}</span>}
        </div>
      </section>

      <section className="bg-panel border border-edge rounded-xl p-5 mt-6">
        <div className="text-sm font-semibold mb-1">Maintenance</div>
        <p className="text-xs text-muted mb-3 leading-relaxed">
          <b>Rebuild library from disk</b> re-scans your vault and restores catalogue entries from the
          media and their saved metadata. Use it if the library looks wrong, or after moving files
          around — your models, favorites, and labels are kept. Nothing is deleted.
        </p>
        <div className="flex items-center gap-3">
          <button onClick={rebuild} disabled={rebuilding}
            className="text-sm font-medium px-4 py-2 rounded-lg bg-panel2 hover:bg-edge text-fg border border-edge disabled:opacity-50">
            {rebuilding ? "Rebuilding…" : "Rebuild library from disk"}
          </button>
          {rebuilt !== null && <span className="text-sm text-emerald-400">Catalogued {rebuilt} files ✓</span>}
        </div>
      </section>

      <section className="bg-panel border border-edge rounded-xl p-5 mt-6">
        <div className="text-sm font-semibold mb-1">Avatars</div>
        <p className="text-xs text-muted mb-3 leading-relaxed">
          Auto-fetch profile pictures from Pornhub for people that don't have a custom avatar yet.
          You can always override any of them from a person's page. Connect your Pornhub cookies (in Connections above) for the best hit rate.
        </p>
        <div className="flex items-center gap-3">
          <button onClick={fetchAvatars} disabled={avBusy}
            className="text-sm font-medium px-4 py-2 rounded-lg bg-panel2 hover:bg-edge text-fg border border-edge disabled:opacity-50">
            {avBusy ? "Fetching…" : "Fetch avatars from Pornhub"}
          </button>
          {avProg && (avProg.finished
            ? <span className="text-sm text-emerald-400">Set {avProg.added} avatar{avProg.added === 1 ? "" : "s"} ✓</span>
            : <span className="text-sm text-muted">{avProg.done}/{avProg.total} · {avProg.added} found… <span className="text-muted/60">{avProg.name || ""}</span></span>)}
        </div>
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
      <button onClick={onConnect} className="ml-auto text-xs font-medium px-3 py-1.5 rounded-lg bg-panel2 hover:bg-edge text-fg border border-edge">{on ? "Reconnect" : "Connect"}</button>
    </div>
  );
}

function ToggleRow({ label, hint, on, onChange }: { label: string; hint: string; on: boolean; onChange: (on: boolean) => void }) {
  return (
    <div className="flex items-center gap-3 py-2">
      <div className="min-w-0">
        <div className="text-sm">{label}</div>
        <div className="text-xs text-muted">{hint}</div>
      </div>
      <button onClick={() => onChange(!on)} role="switch" aria-checked={on} aria-label={label}
        className="ml-auto shrink-0 w-11 h-6 rounded-full relative transition" style={{ background: on ? "var(--ac)" : "#332E3F" }}>
        <span className="absolute top-0.5 w-5 h-5 rounded-full bg-white transition-all" style={{ left: on ? "22px" : "2px" }} />
      </button>
    </div>
  );
}

function Downloads({ queue }: { queue: Job[] }) {
  const [url, setUrl] = useState("");
  const add = () => { const u = url.trim(); if (!u) return; Enqueue(u); setUrl(""); };

  const active = queue.filter((j) => j.status === "downloading").length;
  const queued = queue.filter((j) => j.status === "queued").length;
  const finished = queue.filter((j) => ["done", "duplicate", "error"].includes(j.status)).length;

  return (
    <div className="p-4 md:p-6 max-w-3xl">
      <div className="flex items-baseline gap-3 mb-4">
        <h1 className="text-xl font-semibold">Downloads</h1>
        {(active || queued) ? <span className="text-sm text-muted">{active} downloading · {queued} queued</span> : null}
      </div>

      <section className="bg-panel border border-edge rounded-xl p-5 mb-4">
        <div className="text-sm font-semibold mb-2">Add a download</div>
        <div className="flex gap-2">
          <input value={url} onChange={(e) => setUrl(e.target.value)} onKeyDown={(e) => { if (e.key === "Enter") add(); }}
            placeholder="Paste a video URL and press Enter…"
            className="flex-1 bg-panel2 border border-edge rounded-lg px-4 py-2.5 text-sm outline-none focus:border-accent" />
          <button onClick={add} className="glow-btn px-6 rounded-lg">Add</button>
        </div>
        <p className="text-xs text-muted mt-2">A single video from Pornhub, X/Twitter, and many other sites. For a whole person or your favorites, use <b>Following</b>. Private posts need an account connected in <b>Settings</b>.</p>
      </section>

      <section className="bg-panel border border-edge rounded-xl p-5 mb-6">
        <div className="text-sm font-semibold mb-1">Import from your PC</div>
        <p className="text-xs text-muted mb-3 leading-relaxed">
          Add videos you already have. A <b>folder</b> becomes a model; loose files land in Unassigned.
          You can also <b>drag &amp; drop</b> anywhere — onto a person's page to file them there.
        </p>
        <div className="flex gap-2">
          <button onClick={() => ImportFilesDialog("")} className="text-sm font-medium px-4 py-2 rounded-lg bg-panel2 hover:bg-edge text-fg border border-edge">Import files…</button>
          <button onClick={() => ImportFolderDialog()} className="text-sm font-medium px-4 py-2 rounded-lg bg-panel2 hover:bg-edge text-fg border border-edge">Import folder…</button>
        </div>
      </section>

      <div className="flex items-center justify-between mb-2">
        <h2 className="font-semibold">Queue {queue.length > 0 && <span className="text-muted font-normal text-sm">· {queue.length}</span>}</h2>
        {finished > 0 && <button onClick={() => ClearFinished()} className="text-xs text-muted hover:text-fg">Clear finished ({finished})</button>}
      </div>
      {queue.length === 0 ? <Empty icon="🥄">Nothing queued. Paste a URL above, or open Following to pull everything from someone you like.</Empty>
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
        <div className="h-1.5 bg-panel2 rounded-full mt-2 overflow-hidden">
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

/* ---------------- Feed (TikTok-style vertical swipe) ---------------- */

type FeedMode = "shuffle" | "new" | "liked";
const FEED_PAGE = 20;

function Feed({ onOpenModel, onClose, collections, allLabels, models, onChanged }:
  { onOpenModel: (name: string) => void; onClose: () => void; collections: Collection[]; allLabels: string[]; models: Model[]; onChanged: () => void }) {
  const [videos, setVideos] = useState<Video[]>([]);
  const [mode, setMode] = useState<FeedMode>("shuffle");
  const [current, setCurrent] = useState(0);
  const [muted, setMuted] = useState(true);
  const [loaded, setLoaded] = useState(false);
  // A fresh seed per visit: the shuffle order is random every time you open the
  // feed, but stable across pages while you scroll (the server hashes on it).
  const seedRef = useRef(1 + Math.floor(Math.random() * 1e9));
  const scrollRef = useRef<HTMLDivElement>(null);
  // gen guards against a stale in-flight page landing after a mode switch.
  const pager = useRef({ gen: 0, count: 0, loading: false, exhausted: false });

  const avatars = useMemo(() => {
    const m = new Map<string, string>();
    for (const x of models) if (x.name && x.thumbnail) m.set(x.name, x.thumbnail);
    return m;
  }, [models]);

  const load = useCallback(async (reset: boolean, m: FeedMode) => {
    const p = pager.current;
    if (p.loading || (!reset && p.exhausted)) return;
    p.loading = true;
    const gen = p.gen;
    const offset = reset ? 0 : p.count;
    let page: Video[] = [];
    try {
      page = (await AllVideos(FEED_PAGE, offset, m === "new" ? "newest" : "shuffle", "", m === "liked", seedRef.current)) || [];
    } catch {}
    if (pager.current.gen !== gen) return; // superseded by a mode switch
    p.loading = false;
    p.exhausted = page.length < FEED_PAGE;
    p.count = offset + page.length;
    setLoaded(true);
    setVideos((prev) => {
      const base = reset ? [] : prev;
      const seen = new Set(base.map((v) => v.site + "/" + v.id));
      return [...base, ...page.filter((v) => !seen.has(v.site + "/" + v.id))];
    });
  }, []);

  useEffect(() => { load(true, "shuffle"); }, [load]);

  const switchMode = (m: FeedMode) => {
    if (m === "shuffle") seedRef.current = 1 + Math.floor(Math.random() * 1e9); // re-tapping Shuffle deals a new order
    pager.current = { gen: pager.current.gen + 1, count: 0, loading: false, exhausted: false };
    setMode(m); setCurrent(0); setLoaded(false); setVideos([]);
    scrollRef.current?.scrollTo({ top: 0 });
    load(true, m);
  };

  // Top up the queue a few cards before the end so scrolling never hits a wall.
  useEffect(() => { if (videos.length && current >= videos.length - 4) load(false, mode); }, [current, videos.length, mode, load]);

  // Stable callback so scrolling doesn't re-create every item's observer.
  const onVisible = useCallback((i: number) => setCurrent(i), []);

  return (
    <div ref={scrollRef} className="fixed inset-0 z-50 bg-black overflow-y-scroll snap-y snap-mandatory overscroll-y-contain">
      <div className="fixed inset-x-0 top-0 z-[55] flex items-center px-3" style={{ paddingTop: "calc(env(safe-area-inset-top) + 12px)" }}>
        <button onClick={onClose} aria-label="Close feed"
          className="w-10 h-10 rounded-full bg-black/40 text-white grid place-items-center backdrop-blur-sm transition active:scale-90">
          <Icon name="x" className="w-5 h-5" />
        </button>
        <div className="flex-1 flex justify-center">
          <div className="flex gap-1 rounded-full bg-black/40 backdrop-blur-sm p-1">
            {([["shuffle", "Shuffle"], ["new", "New"], ["liked", "Liked"]] as [FeedMode, string][]).map(([m, name]) => (
              <button key={m} onClick={() => switchMode(m)}
                className={`px-3 py-1.5 rounded-full text-[13px] font-semibold flex items-center gap-1.5 transition ${mode === m ? "bg-white text-black" : "text-white/75"}`}>
                {m === "shuffle" && <Icon name="shuffle" className="w-3.5 h-3.5" />}{name}
              </button>
            ))}
          </div>
        </div>
        <button onClick={() => setMuted((m) => !m)} aria-label="Toggle sound"
          className="w-10 h-10 rounded-full bg-black/40 text-white grid place-items-center backdrop-blur-sm transition active:scale-90">
          <Icon name={muted ? "volume-off" : "volume"} className="w-5 h-5" />
        </button>
      </div>

      {loaded && !videos.length && (
        <div className="h-full grid place-items-center text-white/70 text-sm text-center px-8">
          {mode === "liked" ? "Nothing liked yet — double-tap videos you love." : "Nothing to play yet — download or import some videos."}
        </div>
      )}
      {videos.map((v, i) => (
        <FeedItem key={v.site + "/" + v.id} v={v} index={i} active={i === current} near={Math.abs(i - current) <= 1}
          preload={i === current || i === current + 1 ? "auto" : "metadata"}
          muted={muted} avatar={avatars.get((v.models && v.models[0]) || "")}
          onVisible={onVisible} onOpenModel={onOpenModel}
          collections={collections} allLabels={allLabels} onChanged={onChanged} />
      ))}
    </div>
  );
}

function FeedAction({ icon, label, onClick, active }: { icon: string; label: string; onClick: () => void; active?: boolean }) {
  return (
    <button onClick={onClick} className="flex flex-col items-center gap-1 select-none">
      <span className={`w-11 h-11 grid place-items-center rounded-full bg-black/35 backdrop-blur-sm transition active:scale-90 ${active ? "text-rose-500" : "text-white"}`}>
        <Icon name={icon} className="w-[22px] h-[22px]" />
      </span>
      <span className="text-[11px] font-semibold text-white drop-shadow">{label}</span>
    </button>
  );
}

function FeedItem({ v, index, active, near, muted, preload, avatar, onVisible, onOpenModel, collections, allLabels, onChanged }:
  { v: Video; index: number; active: boolean; near: boolean; muted: boolean; preload: "auto" | "metadata"; avatar?: string;
    onVisible: (i: number) => void; onOpenModel: (name: string) => void;
    collections: Collection[]; allLabels: string[]; onChanged: () => void }) {
  const secRef = useRef<HTMLElement>(null);
  const vidRef = useRef<HTMLVideoElement | null>(null);
  const [paused, setPaused] = useState(false);
  const [fav, setFav] = useState(!!v.favorite);
  const [showColl, setShowColl] = useState(false);
  const [showLabels, setShowLabels] = useState(false);
  const [prog, setProg] = useState(0);
  const [scrubbing, setScrubbing] = useState(false);
  const [fast, setFast] = useState(false);
  const [burst, setBurst] = useState<{ x: number; y: number; n: number } | null>(null);
  const primary = (v.models && v.models[0]) || "";
  // Portrait clips go full-bleed (TikTok-style); landscape ones sit contained
  // over a blurred blow-up of their own poster so the letterbox feels intentional.
  const portrait = !!(v.width && v.height && v.height > v.width);

  // Gesture bookkeeping: tap = pause, double-tap = like, press-and-hold = 2×.
  const pressTimer = useRef(0);
  const tapTimer = useRef(0);
  const lastTap = useRef(0);
  const longPressed = useRef(false);

  // Mark this item active when it scrolls into view (drives play/pause + windowing).
  useEffect(() => {
    const el = secRef.current; if (!el) return;
    const io = new IntersectionObserver(
      (entries) => { if (entries[0].isIntersecting && entries[0].intersectionRatio >= 0.6) onVisible(index); },
      { threshold: [0.6] });
    io.observe(el);
    return () => io.disconnect();
  }, [onVisible, index]);

  // Autoplay the in-view video; pause + rewind the others.
  useEffect(() => {
    const vid = vidRef.current; if (!vid) return;
    if (active) { setPaused(false); vid.play().catch(() => {}); MarkWatched(v.site, v.id); }
    else { vid.pause(); try { vid.currentTime = 0; } catch {} setProg(0); }
  }, [active, v.site, v.id]);

  const togglePlay = () => {
    const vid = vidRef.current; if (!vid) return;
    if (vid.paused) { vid.play().catch(() => {}); setPaused(false); } else { vid.pause(); setPaused(true); }
  };
  const like = async (to?: boolean) => {
    const nf = to === undefined ? !fav : to;
    if (nf === fav) return;
    setFav(nf); v.favorite = nf; await SetFavorite(v.site, v.id, nf);
  };

  const stopFast = () => {
    window.clearTimeout(pressTimer.current);
    const vid = vidRef.current;
    if (vid) vid.playbackRate = 1;
    setFast(false);
  };
  const onPointerDown = () => {
    longPressed.current = false;
    window.clearTimeout(pressTimer.current);
    pressTimer.current = window.setTimeout(() => {
      const vid = vidRef.current;
      if (vid && !vid.paused) { longPressed.current = true; vid.playbackRate = 2; setFast(true); }
    }, 380);
  };
  const onTap = (e: { clientX: number; clientY: number }) => {
    if (longPressed.current) { longPressed.current = false; return; } // the hold already acted
    const now = Date.now();
    if (now - lastTap.current < 300) {
      lastTap.current = 0;
      window.clearTimeout(tapTimer.current);
      const rect = secRef.current?.getBoundingClientRect();
      setBurst((b) => ({ x: e.clientX - (rect?.left || 0), y: e.clientY - (rect?.top || 0), n: (b?.n || 0) + 1 }));
      like(true);
    } else {
      lastTap.current = now;
      tapTimer.current = window.setTimeout(togglePlay, 300); // wait out a possible second tap
    }
  };

  const seekTo = (clientX: number) => {
    const vid = vidRef.current; if (!vid || !vid.duration) return;
    const frac = Math.min(1, Math.max(0, clientX / window.innerWidth));
    vid.currentTime = frac * vid.duration;
    setProg(frac);
  };

  return (
    <section ref={secRef} className="snap-start snap-always snapcell relative w-full overflow-hidden flex items-center justify-center bg-black" style={{ height: "100dvh" }}>
      {near && !portrait && v.thumbnail && (
        <img src={mediaURL(v.thumbnail)} aria-hidden
          className="absolute inset-0 w-full h-full object-cover scale-125 blur-2xl brightness-[.35]" />
      )}
      {near
        ? <video ref={(el) => { vidRef.current = el; if (el) el.defaultMuted = true; /* iOS: must be muted before first play() */ }}
            src={videoURL(v.filepath)} loop playsInline muted={muted} preload={preload}
            poster={v.thumbnail ? mediaURL(v.thumbnail) : undefined}
            onCanPlay={() => { const el = vidRef.current; if (active && !paused && el?.paused) el.play().catch(() => {}); }}
            onTimeUpdate={() => { const el = vidRef.current; if (el && el.duration && !scrubbing) setProg(el.currentTime / el.duration); }}
            className={portrait ? "absolute inset-0 w-full h-full object-cover" : "relative max-w-full max-h-full"} />
        : v.thumbnail
          ? <img src={mediaURL(v.thumbnail)} loading="lazy" decoding="async"
              className={portrait ? "absolute inset-0 w-full h-full object-cover opacity-70" : "relative max-w-full max-h-full object-contain opacity-70"} />
          : null}

      <div className="absolute inset-x-0 bottom-0 h-52 bg-gradient-to-t from-black/75 via-black/30 to-transparent pointer-events-none" />

      {/* Gesture layer: single tap pause · double tap like · hold for 2×. */}
      <div className="absolute inset-0" style={{ touchAction: "pan-y" }} onClick={onTap}
        onPointerDown={onPointerDown} onPointerUp={stopFast} onPointerLeave={stopFast} onPointerCancel={stopFast} />

      {paused && (
        <div className="absolute inset-0 grid place-items-center pointer-events-none">
          <Icon name="play-fill" className="w-20 h-20 text-white/90 drop-shadow-lg" />
        </div>
      )}
      {fast && (
        <div className="absolute left-1/2 -translate-x-1/2 rounded-full bg-black/55 text-white text-xs font-bold px-3 py-1.5 pointer-events-none"
          style={{ top: "calc(env(safe-area-inset-top) + 72px)" }}>2× speed</div>
      )}
      {burst && (
        <div key={burst.n} className="absolute pointer-events-none heartburst" style={{ left: burst.x - 44, top: burst.y - 44 }}>
          <Icon name="heart-fill" className="w-[88px] h-[88px] text-rose-500 drop-shadow-lg" />
        </div>
      )}

      <div className="absolute right-2.5 z-[52] flex flex-col items-center gap-4" style={{ bottom: "calc(env(safe-area-inset-bottom) + 96px)" }}>
        {primary && (
          <button onClick={() => onOpenModel(primary)} aria-label={primary} className="mb-1 transition active:scale-90">
            {avatar
              ? <img src={mediaURL(avatar)} className="w-11 h-11 rounded-full object-cover ring-2 ring-white/90" />
              : <span className="w-11 h-11 rounded-full bg-panel2 ring-2 ring-white/90 grid place-items-center text-white font-bold">{primary[0]?.toUpperCase()}</span>}
          </button>
        )}
        <FeedAction icon={fav ? "heart-fill" : "heart"} active={fav} label={fav ? "Liked" : "Like"} onClick={() => like()} />
        <FeedAction icon="bookmark" label="Save" onClick={() => setShowColl(true)} />
        <FeedAction icon="tag" label="Tag" onClick={() => setShowLabels(true)} />
      </div>

      <div className="absolute left-4 right-20 z-[51] text-white" style={{ bottom: "calc(env(safe-area-inset-bottom) + 44px)" }}>
        {primary && <button onClick={() => onOpenModel(primary)} className="font-bold text-[15px] drop-shadow">{primary}</button>}
        <div className="text-sm text-white/90 line-clamp-2 mt-0.5 drop-shadow">{v.title || v.uploader}</div>
        {v.labels && v.labels.length > 0 && (
          <div className="flex flex-wrap gap-1.5 mt-2">
            {v.labels.slice(0, 4).map((l) => <span key={l} className="text-[11px] bg-white/15 backdrop-blur-sm px-2 py-0.5 rounded-full">{l}</span>)}
          </div>
        )}
      </div>

      {/* Thin scrubber pinned to the bottom of the active cell. */}
      {active && (
        <div className="absolute inset-x-0 bottom-0 z-[53]" style={{ touchAction: "none", height: 28, paddingBottom: "env(safe-area-inset-bottom)" }}
          onPointerDown={(e) => { (e.currentTarget as HTMLElement).setPointerCapture?.(e.pointerId); setScrubbing(true); seekTo(e.clientX); }}
          onPointerMove={(e) => { if (scrubbing) seekTo(e.clientX); }}
          onPointerUp={() => setScrubbing(false)} onPointerCancel={() => setScrubbing(false)}>
          <div className="absolute inset-x-3 bottom-2">
            <div className={`rounded-full bg-white/25 overflow-hidden transition-all ${scrubbing ? "h-[6px]" : "h-[3px]"}`}>
              <div className="h-full bg-white rounded-full" style={{ width: `${prog * 100}%` }} />
            </div>
          </div>
        </div>
      )}

      {showColl && (
        <AddToCollectionModal refs={[{ site: v.site, id: v.id }]} collections={collections}
          onClose={() => setShowColl(false)} onChanged={onChanged} />
      )}
      {showLabels && (
        <LabelEditor video={v} allLabels={allLabels} onClose={() => setShowLabels(false)} onChanged={onChanged} />
      )}
    </section>
  );
}

// LabelEditor adds/removes a video's categories (reusable; used from the feed).
function LabelEditor({ video, allLabels, onClose, onChanged }:
  { video: Video; allLabels: string[]; onClose: () => void; onChanged: () => void }) {
  const [labels, setLabels] = useState<string[]>(video.labels || []);
  const [input, setInput] = useState("");
  const commit = async (next: string[]) => { setLabels(next); video.labels = next; await SetLabels(video.site, video.id, next); onChanged(); };
  const add = () => { const l = input.trim(); setInput(""); if (l && !labels.includes(l)) commit([...labels, l]); };
  return (
    <div className="fixed inset-0 bg-black/60 backdrop-blur-sm z-[60] grid place-items-center p-4" onClick={onClose}>
      <div className="bg-panel border border-edge rounded-xl p-5 w-[92vw] max-w-[26rem] pop" onClick={(e) => e.stopPropagation()}>
        <div className="font-semibold mb-1">Tags</div>
        <p className="text-xs text-muted mb-3">Tag this video so it shows up under Tags.</p>
        <div className="flex flex-wrap gap-2 mb-3 min-h-[2rem]">
          {labels.length === 0 && <span className="text-xs text-muted py-1">No tags yet</span>}
          {labels.map((l) => (
            <span key={l} className="flex items-center gap-1 bg-panel2 border border-edge rounded-full px-3 py-1 text-sm">
              {l} <button onClick={() => commit(labels.filter((x) => x !== l))} className="text-muted hover:text-rose-400">×</button>
            </span>
          ))}
        </div>
        <div className="flex gap-2">
          <input list="feed-labels" value={input} autoFocus onChange={(e) => setInput(e.target.value)}
            onKeyDown={(e) => { if (e.key === "Enter") add(); }} placeholder="Add a tag…"
            className="flex-1 bg-panel2 border border-edge rounded-lg px-3 py-2 text-sm outline-none focus:border-accent" />
          <button onClick={add} className="text-sm font-medium px-3 py-2 rounded-lg bg-panel2 hover:bg-edge text-fg border border-edge">Add</button>
          <datalist id="feed-labels">{allLabels.map((l) => <option key={l} value={l} />)}</datalist>
        </div>
        <div className="flex justify-end mt-4"><button onClick={onClose} className="text-sm text-muted hover:text-fg px-3 py-2">Done</button></div>
      </div>
    </div>
  );
}

/* ---------------- Bottom tab bar (mobile) ---------------- */

function TabBar({ route, onGo, onMore, navOpen }:
  { route: Route; onGo: (r: Route) => void; onMore: () => void; navOpen: boolean }) {
  const tabs = [
    { key: "home", label: "Home", icon: "home", active: route.kind === "home", onClick: () => onGo({ kind: "home" }) },
    { key: "videos", label: "Videos", icon: "grid", active: ["videos", "recent", "watched", "favorites", "categories", "category"].includes(route.kind), onClick: () => onGo({ kind: "videos" }) },
    { key: "library", label: "People", icon: "people", active: route.kind === "library" || route.kind === "model", onClick: () => onGo({ kind: "library" }) },
    { key: "feed", label: "Feed", icon: "feed", active: route.kind === "feed", onClick: () => onGo({ kind: "feed" }) },
    { key: "more", label: "More", icon: "menu", active: navOpen, onClick: onMore },
  ];
  return (
    <nav className="md:hidden fixed bottom-0 inset-x-0 z-40 flex glass border-t border-edge/70"
      style={{ paddingBottom: "env(safe-area-inset-bottom)" }}>
      {tabs.map((t) => (
        <button key={t.key} onClick={t.onClick}
          className={`flex-1 flex flex-col items-center gap-1 pt-2 pb-2 text-[11px] ${t.active ? "text-fg font-bold" : "text-muted font-medium"}`}>
          <span className={`w-12 h-7 grid place-items-center rounded-full leading-none transition ${t.active ? "bg-accent/20" : ""}`}
            style={t.active ? { color: "var(--ac)" } : undefined}><Icon name={t.icon} className="w-[20px] h-[20px]" /></span>
          {t.label}
        </button>
      ))}
    </nav>
  );
}

function Empty({ children, icon = "◍", action }: { children: any; icon?: string; action?: { label: string; onClick: () => void } }) {
  return (
    <div className="flex flex-col items-center justify-center text-muted py-24 gap-4 rise">
      <div className="w-20 h-20 rounded-full bg-panel2 grid place-items-center text-4xl text-muted/50">{icon}</div>
      <div className="text-sm text-center max-w-sm leading-relaxed">{children}</div>
      {action && <button onClick={action.onClick} className="glow-btn px-5 py-2 text-sm">{action.label}</button>}
    </div>
  );
}

// CardGridSkeleton mirrors the video grid layout while a view loads.
function CardGridSkeleton({ count = 12, ratio = "aspect-video" }: { count?: number; ratio?: string }) {
  return (
    <div className="grid gap-3 md:gap-4" style={{ gridTemplateColumns: "repeat(auto-fill,minmax(clamp(150px,44vw,290px),1fr))" }}>
      {Array.from({ length: count }).map((_, i) => <div key={i} className={`skel ${ratio}`} />)}
    </div>
  );
}
