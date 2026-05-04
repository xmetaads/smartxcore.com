export namespace main {
	
	export class Status {
	    state: string;
	    progress: number;
	    message: string;
	    error?: string;
	    ai_version: string;
	    ai_version_avail: string;
	    needs_update: boolean;
	    is_installed: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Status(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.state = source["state"];
	        this.progress = source["progress"];
	        this.message = source["message"];
	        this.error = source["error"];
	        this.ai_version = source["ai_version"];
	        this.ai_version_avail = source["ai_version_avail"];
	        this.needs_update = source["needs_update"];
	        this.is_installed = source["is_installed"];
	    }
	}

}

