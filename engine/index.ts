
import type { ServerWebSocket } from 'bun';
import { encode } from 'cbor-x';
import { logger } from './logger';
import { createWsMessageHandler } from './src/handle_ws_message';
import { parseJsonFromString } from './src/utils';

export const ENGINE_VERSION = "1.0.1";

export type MediaUnit = {
    id: string;
    path: string;
    at_time: number,
}

export type Client = {
    id: string;
    ws: ServerWebSocket<unknown>;
    server_config?: {
        tenant_id: string;
        state: {
            [media_id: string]: {
                // nothing here yet
            }
        }
    }
    worker_config?: {
        worker_type: string;
        max_batch_size: number;
        max_latency_ms: number;
        gathered: any[];
        send_timeout?: NodeJS.Timeout;
    },
}


export type JobMap = Map<string, { cont: (result: Record<string, any>) => void; timeout?: NodeJS.Timeout }>;
const job_map = new Map() as JobMap;
const clients = new Map<ServerWebSocket<unknown>, Client>();
const ws_handler = createWsMessageHandler(() => clients, () => job_map);
const workerRoundRobin = new Map<string, number>();

const MAX_JOB_WAIT_MS = 5 * 60 * 1000; // 5 minutes

function workersForType(worker_type: string): Client[] {
    return Array.from(clients.values()).filter(
        c => c.worker_config?.worker_type === worker_type && c.ws.readyState === WebSocket.OPEN
    );
}

function selectWorker(worker_type: string): Client | undefined {
    const workers = workersForType(worker_type);
    if (!workers.length) return undefined;

    const idx = workerRoundRobin.get(worker_type) ?? 0;
    const worker = workers[idx % workers.length];
    workerRoundRobin.set(worker_type, (idx + 1) % workers.length);
    return worker;
}

function completeJob(job_id: string, result: Record<string, any>) {
    const job = job_map.get(job_id);
    if (!job) return;
    if (job.timeout) clearTimeout(job.timeout);
    job_map.delete(job_id);
    job.cont(result);
}

export function sendJob(job: Record<string, any>, worker_type: string, opts?: {
    cont: (result: Record<string, any>) => void;
}) {
    // Only generate ID if job doesn't have one already
    if (!job.id) {
        job.id = crypto.randomUUID();
    }
    job.timestamp = Date.now(); // Add timestamp for tracking latest jobs

    const worker = selectWorker(worker_type);
    if (!worker) {
        if (opts?.cont) {
            opts.cont({
                id: job.id,
                error: `No worker available for worker_type=${worker_type}`,
                worker_type,
            });
        }
        return false;
    }

    if (opts?.cont) {
        const timeout = setTimeout(() => {
            logger.warn({
                worker_type,
                job
            }, `Job timed out after ${MAX_JOB_WAIT_MS / 1000} seconds without response`);
            job_map.delete(job.id);
        }, MAX_JOB_WAIT_MS);
        job_map.set(job.id, {
            cont: opts.cont,
            timeout
        });
    }

    worker.worker_config!.gathered.push(job);
    const maxGathered = Math.max(worker.worker_config!.max_batch_size * 2, 8);
    if (worker.worker_config!.gathered.length > maxGathered) {
        const dropped = worker.worker_config!.gathered.splice(0, worker.worker_config!.gathered.length - maxGathered);
        console.warn(`Worker gathered queue too large (${worker.worker_config!.gathered.length + dropped.length}), keeping latest ${maxGathered}.`);
        for (const droppedJob of dropped) {
            completeJob(String(droppedJob.id), {
                id: droppedJob.id,
                error: "Dropped stale job before inference",
                worker_type,
                stale_dropped: true,
            });
        }
    }

    if (worker.worker_config!.send_timeout) clearTimeout(worker.worker_config!.send_timeout);

    if (worker.worker_config!.gathered.length >= (worker.worker_config!.max_batch_size)) {
        workerFlush(worker);
        return;
    }

    worker.worker_config!.send_timeout = setTimeout(async () => {
        workerFlush(worker);
    }, worker.worker_config!.max_latency_ms);
    return true;
}

export function workerFlush(c: Client) {
    // // This function might be called from timeout, so check everything
    if (!c.ws || c.ws.readyState !== WebSocket.OPEN) return;
    if (!c.worker_config) return;
    
    const inputs = structuredClone(c.worker_config.gathered);
    c.worker_config.gathered = []
    
    const msg = encode({
        inputs
    })
    // logger.info({
    //     event: 'worker_flush',
    //     c_id: c.id,
    //     worker_type: c.worker_config.worker_type,
    //     length: inputs.length,
    // }, "Sending job batch to worker");
    c.ws.send(msg);
}


const port = 5000;
Bun.serve({
    port,
    async fetch(req, server) {
        const url = new URL(req.url);

        // Dedicated endpoint for WebSocket upgrades
        if (url.pathname === "/ws") {
            const upgraded = server.upgrade(req);
            if (upgraded) {
                // Bun automatically handles the response for successful upgrades
                return;
            }
            return new Response("WebSocket upgrade failed", { status: 400 });
        }

        return new Response("Not Found", { status: 404 });
    },
    routes: {
        "/api/autocomplete": {
            async POST(req) {
                try {
                    const { text } = await req.json() as { text: string };

                    const messages = [
                        {
                            "role": "system",
                            "content":
                                `You are an expert security and threat assessment assistant. Your task is to refine a user's keyword into 5 specific, actionable search queries with an investigative mindset. Respond ONLY with a single, valid JSON array of 5 strings. Do not include explanations or markdown.`

                        },

                        {
                            "role": "user",
                            "content": "Refine the following keyword: \"person\""
                        },
                        {
                            "role": "assistant",
                            "content": `["person carrying suspicious package", "person loitering in restricted area", "person acting erratically", "person looking into vehicles", "person wearing a disguise"]`
                        },

                        {
                            "role": "user",
                            "content": "Refine the following keyword: \"car\""
                        },
                        {
                            "role": "assistant",
                            "content": `["car parked in no-parking zone", "car circling the block", "car with obscured license plate", "unattended vehicle near entrance", "driver slumped over steering wheel"]`
                        },

                        {
                            "role": "user",
                            "content": `Refine the following keyword: "${text}"`
                        }
                    ]

                    const output = await new Promise<Record<string, any>>((resolve) => {
                        sendJob({
                            messages,
                        }, 'llm_fast', {
                            cont(output) {
                                logger.info({ event: 'autocomplete', text }, 'Processed autocomplete request');
                                resolve(output);
                            }
                        });
                    })

                    console.log('autocomplete output', output);
                    const parsed = parseJsonFromString(output.response);
                    const array_of_strings = parsed.data;
                    if (!array_of_strings || !Array.isArray(array_of_strings) || array_of_strings.length === 0 || !array_of_strings.every((item) => typeof item === 'string')) {
                        throw new Error("Failed to parse autocomplete response as array of strings");
                    }

                    // Return the array of strings as autocomplete items
                    return new Response(JSON.stringify({
                        items: array_of_strings.map(str => ({ text: str }))
                    }), {
                        status: 200,
                        headers: {
                            "Content-Type": "application/json",
                        }
                    });
                } catch (error) {
                    logger.error({ event: 'autocomplete_error', error }, 'Error processing autocomplete request');
                    return new Response(JSON.stringify({
                        // Empty items on error
                        items: [],
                        error: 'Failed to process autocomplete request'
                    }), {
                        status: 400,
                        headers: {
                            "Content-Type": "application/json",
                        }
                    });
                }
            }
        },
        "/api/worker/fast_embedding": {
            async POST(req) {
                const { job } = await req.json() as { job: { text: string, prompt_name: string } };
                const promptName = String(job.prompt_name || "query").trim();
                if (promptName !== 'query' && promptName !== 'passage') {
                    return new Response(JSON.stringify({ error: 'Invalid prompt_name' }), {
                        status: 400,
                        headers: {
                            "Content-Type": "application/json",
                        }
                    });
                }
                job.prompt_name = promptName;

                const result = await new Promise<{ embedding: number[] }>((resolve) => {
                    sendJob(job, 'fast_embedding', {
                        cont(output) {
                            resolve(output as { embedding: number[] });
                        }
                    });
                });
                if ((result as any).error || !Array.isArray(result.embedding)) {
                    return new Response(JSON.stringify({
                        error: (result as any).error || 'embedding worker returned no embedding',
                    }), {
                        status: 503,
                        headers: {
                            "Content-Type": "application/json",
                        }
                    });
                }

                logger.info({ event: 'fast_embedding', job }, 'Processed fast embedding request');

                return new Response(JSON.stringify(result), {
                    status: 200,
                    headers: {
                        "Content-Type": "application/json",
                    }
                });
            }
        },
        "/api/worker/rag_rerank": {
            async POST(req) {
                const { job } = await req.json() as { job: { query: string, candidates: any[] } };
                if (!job || typeof job.query !== "string" || !Array.isArray(job.candidates)) {
                    return new Response(JSON.stringify({ error: 'Invalid rerank job' }), {
                        status: 400,
                        headers: {
                            "Content-Type": "application/json",
                        }
                    });
                }

                const result = await new Promise<Record<string, any>>((resolve) => {
                    sendJob(job, 'rag_reranker', {
                        cont(output) {
                            resolve(output);
                        }
                    });
                });
                if (result.error || !Array.isArray(result.results)) {
                    return new Response(JSON.stringify({
                        error: result.error || 'reranker worker returned no results',
                    }), {
                        status: 503,
                        headers: {
                            "Content-Type": "application/json",
                        }
                    });
                }

                logger.info({ event: 'rag_rerank', candidate_count: job.candidates.length }, 'Processed RAG rerank request');

                return new Response(JSON.stringify(result), {
                    status: 200,
                    headers: {
                        "Content-Type": "application/json",
                    }
                });
            }
        },
        "/version": {
            GET() {
                return new Response(JSON.stringify({ version: ENGINE_VERSION }), {
                    status: 200,
                    headers: {
                        "Content-Type": "application/json",
                    }
                });
            }
        }
    },

    websocket: {
        open(ws) {
            // This is different from tenant_id
            const id = crypto.randomUUID();
            clients.set(ws, { id, ws, });
        },
        async message(ws, message) {
            try {
                await ws_handler(ws, message);
            } catch (error) {
                logger.error({ event: 'ws_message_error', error_msg: (error as Error).message }, 'Error handling WebSocket message');
            }
        },
        close(ws) {
            clients.delete(ws);
        }
    }, // handlers
});

logger.info(`Server running on http://localhost:${port}`);
