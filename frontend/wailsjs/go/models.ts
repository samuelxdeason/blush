export namespace downloader {
	
	export class Job {
	    id: string;
	    url: string;
	    title: string;
	    status: string;
	    percent: number;
	    speed: string;
	    eta: string;
	    count: number;
	    error: string;
	
	    static createFrom(source: any = {}) {
	        return new Job(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.url = source["url"];
	        this.title = source["title"];
	        this.status = source["status"];
	        this.percent = source["percent"];
	        this.speed = source["speed"];
	        this.eta = source["eta"];
	        this.count = source["count"];
	        this.error = source["error"];
	    }
	}
	export class RemoteItem {
	    url: string;
	    title: string;
	    id: string;
	    owned: boolean;
	
	    static createFrom(source: any = {}) {
	        return new RemoteItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.url = source["url"];
	        this.title = source["title"];
	        this.id = source["id"];
	        this.owned = source["owned"];
	    }
	}

}

export namespace library {
	
	export class LabelCount {
	    label: string;
	    count: number;
	
	    static createFrom(source: any = {}) {
	        return new LabelCount(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.label = source["label"];
	        this.count = source["count"];
	    }
	}
	export class Model {
	    name: string;
	    count: number;
	    totalSeconds: number;
	    bytes: number;
	    sites: string;
	    thumbnail: string;
	
	    static createFrom(source: any = {}) {
	        return new Model(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.count = source["count"];
	        this.totalSeconds = source["totalSeconds"];
	        this.bytes = source["bytes"];
	        this.sites = source["sites"];
	        this.thumbnail = source["thumbnail"];
	    }
	}
	export class ModelLink {
	    label: string;
	    url: string;
	
	    static createFrom(source: any = {}) {
	        return new ModelLink(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.label = source["label"];
	        this.url = source["url"];
	    }
	}
	export class ModelInfo {
	    name: string;
	    bio: string;
	    links: ModelLink[];
	    cover: string;
	
	    static createFrom(source: any = {}) {
	        return new ModelInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.bio = source["bio"];
	        this.links = this.convertValues(source["links"], ModelLink);
	        this.cover = source["cover"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class Photo {
	    id: string;
	    model: string;
	    filepath: string;
	    filename: string;
	    added: string;
	
	    static createFrom(source: any = {}) {
	        return new Photo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.model = source["model"];
	        this.filepath = source["filepath"];
	        this.filename = source["filename"];
	        this.added = source["added"];
	    }
	}
	export class SiteStat {
	    site: string;
	    count: number;
	    bytes: number;
	
	    static createFrom(source: any = {}) {
	        return new SiteStat(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.site = source["site"];
	        this.count = source["count"];
	        this.bytes = source["bytes"];
	    }
	}
	export class Stats {
	    totalBytes: number;
	    videoCount: number;
	    modelCount: number;
	    sites: SiteStat[];
	
	    static createFrom(source: any = {}) {
	        return new Stats(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.totalBytes = source["totalBytes"];
	        this.videoCount = source["videoCount"];
	        this.modelCount = source["modelCount"];
	        this.sites = this.convertValues(source["sites"], SiteStat);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Video {
	    id: string;
	    site: string;
	    title: string;
	    uploader: string;
	    models: string[];
	    duration?: number;
	    width?: number;
	    height?: number;
	    ext: string;
	    filepath: string;
	    filename: string;
	    thumbnail: string;
	    thumbnail_url: string;
	    webpage_url: string;
	    upload_date: string;
	    view_count?: number;
	    like_count?: number;
	    tags: string[];
	    categories: string[];
	    description: string;
	    filesize?: number;
	    added: string;
	    watched_at: string;
	    favorite: boolean;
	    labels: string[];
	
	    static createFrom(source: any = {}) {
	        return new Video(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.site = source["site"];
	        this.title = source["title"];
	        this.uploader = source["uploader"];
	        this.models = source["models"];
	        this.duration = source["duration"];
	        this.width = source["width"];
	        this.height = source["height"];
	        this.ext = source["ext"];
	        this.filepath = source["filepath"];
	        this.filename = source["filename"];
	        this.thumbnail = source["thumbnail"];
	        this.thumbnail_url = source["thumbnail_url"];
	        this.webpage_url = source["webpage_url"];
	        this.upload_date = source["upload_date"];
	        this.view_count = source["view_count"];
	        this.like_count = source["like_count"];
	        this.tags = source["tags"];
	        this.categories = source["categories"];
	        this.description = source["description"];
	        this.filesize = source["filesize"];
	        this.added = source["added"];
	        this.watched_at = source["watched_at"];
	        this.favorite = source["favorite"];
	        this.labels = source["labels"];
	    }
	}

}

export namespace main {
	
	export class CookieStatus {
	    x: boolean;
	    pornhub: boolean;
	
	    static createFrom(source: any = {}) {
	        return new CookieStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.x = source["x"];
	        this.pornhub = source["pornhub"];
	    }
	}

}

