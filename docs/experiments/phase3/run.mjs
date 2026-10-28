// Operator experiment script. All research is submitted by the existing Go CLI
// to the existing app/queue/DB. This is not a production execution path.
import { spawn, execFileSync } from 'node:child_process';
import { readFile, writeFile, readdir } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { fileURLToPath } from 'node:url';
import { resolve, dirname } from 'node:path';

const dir = dirname(fileURLToPath(import.meta.url));
const root = resolve(dir, '../../..');
const binary = '/tmp/opencode/qagent-phase3';
const llama = '/home/slager/Documents/Codex/2026-10-13/v/artifacts/runtime-vulkan/build/bin/llama-server';
const gguf = '/home/slager/Documents/Codex/2026-10-13/v/artifacts/models/Qwen3.5-4B-Q4_K_M.gguf';
const api = 'http://127.0.0.1:18080';
const appUnit = 'qwenchik-bench-app.service';
const modelUnit = 'qwenchik-bench-model.service';
const ordinary = ['qwenchik-app.service', 'qwenchik-model.service'];
const protocolRaw = await readFile(resolve(dir, 'protocol.json'));
const protocol = JSON.parse(protocolRaw);
const hash = value => createHash('sha256').update(value).digest('hex');
const protocolHash = hash(protocolRaw);
const log = message => console.log(`${new Date().toISOString()} ${message}`);
const sleep = ms => new Promise(r => setTimeout(r, ms));
let stopped = false, child;
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => { stopped = true; child?.kill('SIGINT'); });
const checkStop = () => { if (stopped) throw Error('operator interruption'); };
const exec = (command, args, allowFailure = false) => {
  try { return execFileSync(command, args, { cwd: root, encoding: 'utf8', timeout: 60000, maxBuffer: 4 << 20 }); }
  catch (e) { if (allowFailure) return `${e.stdout ?? ''}\n${e.stderr ?? ''}`; throw e; }
};
const systemctl = (...args) => exec('systemctl', ['--user', ...args]);
const save = (path, value) => writeFile(resolve(dir, path), JSON.stringify(value, null, 2) + '\n', { flag: 'wx', mode: 0o600 });
async function get(path, base = api) {
  const r = await fetch(base + path, { signal: AbortSignal.timeout(8000) });
  if (!r.ok) throw Error(`HTTP ${r.status}: ${path}`);
  return r.json();
}
async function ready(base, path) {
  for (let n = 0; n < 120; n++) {
    checkStop();
    try { return await get(path, base); } catch { await sleep(1000); }
  }
  throw Error(`readiness timeout: ${base}${path}`);
}
function unitState(unit) {
  const raw = systemctl('show', unit, '-p', 'MainPID', '-p', 'NRestarts', '-p', 'Result', '-p', 'ActiveState', '-p', 'MemoryPeak', '-p', 'MemoryMax', '-p', 'MemorySwapMax', '-p', 'Restart', '-p', 'ControlGroup', '-p', 'ExecStart');
  return Object.fromEntries(raw.trim().split('\n').map(line => { const i = line.indexOf('='); return [line.slice(0, i), line.slice(i + 1)]; }));
}
async function resourceSample(modelPID = 0, appPID = 0) {
  const sample = { at: new Date().toISOString(), ram_available_bytes: null, model_rss_bytes: null, app_rss_bytes: null, gpu_free_mib: null, gpu_temperature_c: null, ac_online: null, errors: [] };
  try { const text = await readFile('/proc/meminfo', 'utf8'); sample.ram_available_bytes = Number(text.match(/^MemAvailable:\s+(\d+)/m)[1]) * 1024; } catch { sample.errors.push('ram_unavailable'); }
  for (const [pid, key] of [[modelPID, 'model_rss_bytes'], [appPID, 'app_rss_bytes']]) {
    if (!pid) continue;
    try { sample[key] = Number((await readFile(`/proc/${pid}/status`, 'utf8')).match(/^VmRSS:\s+(\d+)/m)[1]) * 1024; } catch { sample.errors.push(key + '_unavailable'); }
  }
  try { const [free, temp] = exec('nvidia-smi', ['--query-gpu=memory.free,temperature.gpu', '--format=csv,noheader,nounits']).trim().split(',').map(Number); sample.gpu_free_mib = free; sample.gpu_temperature_c = temp; } catch { sample.errors.push('gpu_unavailable'); }
  try {
    for (const name of await readdir('/sys/class/power_supply')) {
      const base = `/sys/class/power_supply/${name}`;
      if (['Mains', 'USB', 'USB_C'].includes((await readFile(base + '/type', 'utf8')).trim())) sample.ac_online = sample.ac_online === true || (await readFile(base + '/online', 'utf8')).trim() === '1';
    }
  } catch { sample.errors.push('power_unavailable'); }
  return sample;
}
async function startingEnvelope() {
  for (let n = 0; n < 150; n++) {
    checkStop(); const s = await resourceSample();
    if (s.ac_online === true && s.ram_available_bytes >= protocol.initial_min_available_ram_bytes && s.gpu_temperature_c != null && s.gpu_temperature_c <= protocol.initial_gpu_temperature_max_c) return s;
    await sleep(2000);
  }
  throw Error('safe starting resource/AC envelope not established');
}
const envCommon = {
  APP_BIND_ADDR: '127.0.0.1:18080', DATABASE_PATH: resolve(root, 'data/qagent.db'), ARTIFACTS_PATH: resolve(root, 'data/artifacts'),
  LLM_BASE_URL: 'http://127.0.0.1:8000/v1', LLM_MODEL: 'local-qwen', LLM_CONTEXT_LENGTH: '32768', LLM_MAX_OUTPUT_TOKENS: '4096',
  LLM_TEMPERATURE: '0.2', LLM_TOP_P: '0.8', LLM_ENABLE_THINKING: 'false', LLM_REQUEST_TIMEOUT_SECONDS: '120', LLM_MAX_CONCURRENCY: '1',
  RESEARCH_PRIVACY_MODE: 'offline', RESEARCH_OFFLINE_CORPUS: resolve(root, 'internal/cli/testdata/lab-corpus.json'), RESEARCH_SECTION_REPORT_ENABLED: 'true',
  CONTEXT_PROBE_ENABLED: 'false', LAB_HOLD_RESOURCE_PAUSES: 'true', NOTIFICATION_CHANNEL: 'none', SEARCH_ENABLE_DDG: 'false',
  RESOURCE_GUARD_ENABLED: 'true', MIN_FREE_RAM_BYTES: '536870912', MIN_FREE_VRAM_MB: '256', GPU_TEMP_MAX_C: '85',
  PROCESS_RSS_MAX_BYTES: '2147483648', MODEL_RSS_MAX_BYTES: '3221225472', RESOURCE_CHECK_INTERVAL_SECONDS: '1', KEEP_AWAKE: 'true',
  RESOURCE_MAX_RESUME_ATTEMPTS: '5', WORKER_LEASE_SECONDS: '30', MAX_STEPS: '12', MAX_INITIAL_PLAN_STEPS: '12', MAX_ROUND_STEPS: '12',
  MAX_TOTAL_ROUNDS: '10', MAX_TOTAL_STEPS: '96', MAX_SEARCH_QUERIES: '48', MAX_FETCHES: '36', MAX_LLM_CALLS: '128', MAX_EVIDENCE: '32',
  MAX_QUESTIONS: '80', MAX_QUESTIONS_ACCEPTED: '40', MAX_CITATION_SOURCES: '8', MAX_CITATION_CLAIMS: '32', MAX_REPORT_SECTIONS: '8',
  MAX_REPORT_PARAGRAPHS: '32', MAX_REPORT_BYTES: '524288'
};
async function stopExperimental() {
  exec('systemctl', ['--user', 'stop', appUnit, modelUnit], true);
  for (let n = 0; n < 30; n++) {
    const states = [unitState(appUnit), unitState(modelUnit)];
    if (states.every(s => (!s.MainPID || s.MainPID === '0') && !['active', 'activating', 'deactivating'].includes(s.ActiveState))) break;
    if (n === 29) throw Error('experimental shutdown unconfirmed');
    await sleep(1000);
  }
  exec('systemctl', ['--user', 'reset-failed', appUnit, modelUnit], true);
}
function startModel() {
  checkStop();
  exec('systemd-run', ['--user', '--unit=' + modelUnit, '--property=MemoryMax=4294967296', '--property=MemorySwapMax=0', '--property=Restart=no', '--property=TimeoutStopSec=20',
    llama, '-m', gguf, '--device', 'Vulkan1', '-ngl', '99', '--host', '127.0.0.1', '--port', '8000', '--alias', 'local-qwen', '--jinja', '--parallel', '1', '-c', '32768', '-fa', 'on', '-ctk', 'q8_0', '-ctv', 'q8_0', '--reasoning', 'off', '--no-context-shift', '--no-webui']);
}
function startApp(variant) {
  checkStop();
  const settings = { ...envCommon, RESEARCH_ADAPTIVE_ENABLED: String(variant === 'adaptive') };
  exec('systemd-run', ['--user', '--unit=' + appUnit, '--working-directory=' + root, '--property=MemoryMax=1073741824', '--property=MemorySwapMax=0', '--property=Restart=no', '--property=TimeoutStopSec=20',
    ...Object.entries(settings).flatMap(([k, v]) => ['--setenv=' + k + '=' + v]), binary, 'serve']);
}
async function benchmark(args, env) {
  checkStop();
  return new Promise((resolvePromise, reject) => {
    child = spawn(binary, args, { cwd: root, env: { ...process.env, ...env } });
    let stdout = '', stderr = '';
    child.stdout.on('data', b => { stdout += b; if (stdout.length > 4 << 20) child.kill('SIGINT'); });
    child.stderr.on('data', b => { stderr += b; if (stderr.length > 4 << 20) child.kill('SIGINT'); });
    child.on('error', reject);
    child.on('close', (code, signal) => { child = null; resolvePromise({ exit_code: code, signal, stdout, stderr }); });
  });
}
async function drain(runID) {
  let idle = 0;
  for (let n = 0; n < 30; n++) {
    const health = await get('/api/health');
    const runtime = await get(`/api/runs/${runID}/runtime`);
    const slots = await get('/slots', 'http://127.0.0.1:8000');
    if (!health.runtime?.active_run_id && (['completed', 'failed', 'cancelled'].includes(runtime.status) || runtime.status === 'paused' && runtime.pause_reason === 'manual') && slots.length === 1 && slots[0].is_processing === false) {
      if (++idle >= 2) return;
    } else idle = 0;
    await sleep(1000);
  }
  throw Error('work/llama slot not confirmed drained');
}
async function memoryEvents(state) {
  try { return await readFile('/sys/fs/cgroup' + state.ControlGroup + '/memory.events', 'utf8'); }
  catch { return null; }
}
const slots = protocol.cases.flatMap((id, i) => (i % 2 ? ['adaptive', 'baseline'] : ['baseline', 'adaptive']).map(variant => ({ case_id: id, variant, state: 'unattempted' })));
// Exclusive ledger creation prevents accidental repetition of a frozen experiment.
await save('accounting.initial.json', { protocol_sha256: protocolHash, slots });
if (hash(await readFile(binary)) !== protocol.app_sha256) throw Error('app binary differs from frozen protocol');
const original = Object.fromEntries(ordinary.map(u => [u, systemctl('is-active', u).trim()]));
const beforeTasks = await get('/api/tasks', 'http://127.0.0.1:8080');
if (beforeTasks.some(t => !['completed', 'failed', 'cancelled'].includes(t.status) && !(t.status === 'paused' && t.pause_reason === 'manual'))) throw Error('unrelated eligible work exists');
await save('deployment.json', { protocol_sha256: protocolHash, operator_script_sha256: hash(await readFile(fileURLToPath(import.meta.url))), started_at: new Date().toISOString(), original_services: original, original_tasks: beforeTasks.map(t => ({ id: t.id, status: t.status, pause_reason: t.pause_reason })), environment: envCommon, source: exec('git', ['status', '--short']), node: process.version, model_version: exec(llama, ['--version']), gpu: exec('nvidia-smi', ['--query-gpu=name,driver_version,memory.total', '--format=csv,noheader']) });
let failure = null;
try {
  systemctl('stop', ...ordinary);
  await stopExperimental();
  for (const [index, slot] of slots.entries()) {
    checkStop();
    slot.state = 'attempted'; slot.started_at = new Date().toISOString();
    const prefix = `${String(index + 1).padStart(2, '0')}-${slot.case_id}-${slot.variant}`;
    await save(prefix + '-consumed.json', slot);
    log(`START ${prefix}`);
    const initial = await startingEnvelope();
    startModel(); await ready('http://127.0.0.1:8000', '/health');
    const props = await get('/props', 'http://127.0.0.1:8000');
    const models = await get('/v1/models', 'http://127.0.0.1:8000');
    startApp(slot.variant); await ready(api, '/api/health');
    const policy = await get('/api/lab/policy');
    if (props.default_generation_settings?.n_ctx !== protocol.context || props.total_slots !== 1 || props.model_alias !== protocol.model || !policy.hold_resource_pauses || policy.research_limits?.privacy_mode !== 'offline' || policy.research_limits?.offline_corpus_sha256 !== protocol.corpus_sha256 || policy.research_limits?.adaptive_enabled !== (slot.variant === 'adaptive') || !policy.research_limits?.sectioned_report_enabled) throw Error('deployment/preflight policy mismatch');
    let healthBefore;
    for (let n = 0; n < 30; n++) {
      healthBefore = await get('/api/health');
      if (healthBefore.worker_details?.status === 'fresh') break;
      await sleep(1000);
    }
    if (healthBefore.database !== 'ok' || healthBefore.worker !== 'attached' || healthBefore.worker_details?.status !== 'fresh') throw Error('application not healthy/fresh');
    const inventory = await get('/api/tasks');
    if (inventory.some(t => !['completed', 'failed', 'cancelled'].includes(t.status) && !(t.status === 'paused' && t.pause_reason === 'manual'))) throw Error('eligible unrelated work after startup');
    const modelBefore = unitState(modelUnit), appBefore = unitState(appUnit);
    if (modelBefore.MemoryMax !== String(protocol.model_memory_max_bytes) || modelBefore.MemorySwapMax !== '0' || modelBefore.Restart !== 'no' || appBefore.MemoryMax !== String(protocol.app_memory_max_bytes) || appBefore.MemorySwapMax !== '0') throw Error('cgroup policy mismatch');
    await save(prefix + '-start.json', { initial_resources: initial, model: modelBefore, app: appBefore, policy, props, models });
    const samples = []; let samplingError = null, samplePromise = Promise.resolve(), sampling = false, missedSamples = 0;
    const sample = () => { if (sampling) { missedSamples++; return; } sampling = true; samplePromise = (async () => { if (samples.length >= 1200) throw Error('sample bound exceeded'); samples.push(await resourceSample(Number(modelBefore.MainPID), Number(appBefore.MainPID))); })().catch(e => { samplingError = String(e); }).finally(() => { sampling = false; }); };
    sample(); const timer = setInterval(sample, 2000);
    let result;
    try {
      result = await benchmark(['lab', 'benchmark', '--variant', slot.variant, '--case', slot.case_id, '--case-timeout', '35m', '--protocol-sha256', protocolHash, '--stop-on-pause', '--output', resolve(dir, prefix + '.jsonl')], { ...envCommon, RESEARCH_ADAPTIVE_ENABLED: String(slot.variant === 'adaptive') });
    } finally { clearInterval(timer); sample(); await samplePromise; }
    const modelAfter = unitState(modelUnit), appAfter = unitState(appUnit);
    await save(prefix + '-resources.json', { target_sample_period_seconds: 2, missed_sample_intervals: missedSamples, sampling_error: samplingError, samples });
    await save(prefix + '-process.json', { started_at: slot.started_at, finished_at: new Date().toISOString(), result, model: modelAfter, app: appAfter, model_memory_events: await memoryEvents(modelAfter), app_memory_events: await memoryEvents(appAfter), model_journal: exec('journalctl', ['--user', '-u', modelUnit, '--since', slot.started_at, '--no-pager', '-n', '120'], true) });
    const rawReceipt = await readFile(resolve(dir, prefix + '.jsonl'), 'utf8');
    if (rawReceipt !== result.stdout) throw Error('receipt/stdout mismatch');
    const rows = rawReceipt.trim().split('\n').map(x => JSON.parse(x));
    if (rows.length !== 1) throw Error('expected one case receipt');
    const row = rows[0];
    if (row.case_id !== slot.case_id || row.variant !== slot.variant) throw Error('receipt identity mismatch');
    if (!row?.run_id) throw Error('attempt has no confirmed run identity; retained accounting, no replacement');
    slot.run_id = row.run_id; slot.cli_exit_code = result.exit_code; slot.status = row.status; slot.error_code = row.error_code ?? null;
    await save(prefix + '-attempt.json', slot);
    const runtime = await get(`/api/runs/${row.run_id}/runtime`);
    const run = await get(`/api/runs/${row.run_id}`);
    const artifacts = await get(`/api/tasks/${row.run_id}/artifacts`);
    const events = []; let eventAfter = 0, eventsComplete = false;
    for (let page = 0; page < 8; page++) {
      const batch = await get(`/api/tasks/${row.run_id}/events?after=${eventAfter}`);
      events.push(...batch);
      if (batch.length < 500) { eventsComplete = true; break; }
      const next = Math.max(...batch.map(e => e.seq));
      if (next <= eventAfter) break; eventAfter = next;
    }
    const steps = await get(`/api/tasks/${row.run_id}/steps`);
    const inference = []; let after = 0, complete = false;
    for (let page = 0; page < 8; page++) {
      const p = await get(`/api/runs/${row.run_id}/inference-summaries?after=${after}&until=${runtime.cursor}`);
      inference.push(...p.summaries); if (p.complete) { complete = true; break; }
      if (p.next_after <= after) break; after = p.next_after;
    }
    await save(prefix + '-diagnostics.json', { runtime, run, artifacts, public_events_complete: eventsComplete, events, steps, inference_summaries_complete: complete, inference_summaries: inference });
    await drain(row.run_id);
    if (modelAfter.Result !== 'success' || modelAfter.NRestarts !== '0' || modelAfter.ActiveState !== 'active' || appAfter.ActiveState !== 'active') throw Error('model/app process failed or restarted; stop schedule');
    if (['context_cleanup_unconfirmed', 'benchmark_cleanup_failed', 'benchmark_policy_mismatch', 'benchmark_snapshot_changed', 'runtime_identity_mismatch', 'benchmark_runtime_invalid'].includes(row.error_code)) throw Error('invalid attempt/cleanup; stop schedule');
    slot.state = 'measured'; slot.finished_at = new Date().toISOString();
    await save(prefix + '-accounting.json', slot);
    log(`END ${prefix}: ${row.status} ${row.resource_reason || row.outcome || ''}, ${row.counters?.llm_calls ?? '?'} reservations, ${row.output_bytes} output bytes`);
    await stopExperimental();
  }
} catch (e) { failure = String(e.stack ?? e); log(`STOP ${failure}`); }
finally {
  const restorationErrors = [];
  try { await stopExperimental(); } catch (e) { restorationErrors.push(String(e)); }
  for (const unit of [...ordinary].reverse()) if (original[unit] === 'active') {
    try { systemctl('start', unit); } catch (e) { restorationErrors.push(String(e)); }
  }
  await save('accounting.final.json', { protocol_sha256: protocolHash, finished_at: new Date().toISOString(), failure, restoration_errors: restorationErrors, slots });
  if (restorationErrors.length) failure = [failure, ...restorationErrors].filter(Boolean).join('; ');
  log(`FINISHED measured=${slots.filter(s => s.state === 'measured').length}/24`);
}
if (failure) process.exitCode = 1;
