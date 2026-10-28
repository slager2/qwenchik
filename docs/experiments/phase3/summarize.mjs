// Offline analysis only. Run once AFTER accounting.final.json contains 24 measured slots.
import { readFile, writeFile, lstat } from 'node:fs/promises';
import { createReadStream } from 'node:fs';
import { createHash } from 'node:crypto';
import { spawn } from 'node:child_process';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const dir = dirname(fileURLToPath(import.meta.url)), root = resolve(dir, '../../..');
const binary = '/tmp/opencode/qagent-phase3';
const llama = '/home/slager/Documents/Codex/2026-10-13/v/artifacts/runtime-vulkan/build/bin/llama-server';
const gguf = '/home/slager/Documents/Codex/2026-10-13/v/artifacts/models/Qwen3.5-4B-Q4_K_M.gguf';
const check = (ok, message) => { if (!ok) throw Error(message); };
const sha = data => createHash('sha256').update(data).digest('hex');
const digest = async path => { const h = createHash('sha256'); for await (const b of createReadStream(path)) h.update(b); return h.digest('hex'); };
async function bytes(name, limit = 32 << 20) {
  const path = resolve(dir, name), s = await lstat(path);
  check(s.isFile() && s.size <= limit, `not a bounded regular file: ${name}`);
  return readFile(path);
}
const json = async name => JSON.parse(await bytes(name));
const write = (name, value) => writeFile(resolve(dir, name), JSON.stringify(value, null, 2) + '\n', { flag: 'wx', mode: 0o600 });
const canonical = x => JSON.stringify(x, (_, v) => v && !Array.isArray(v) && typeof v === 'object' ? Object.fromEntries(Object.entries(v).sort(([a], [b]) => a.localeCompare(b))) : v);
const number = x => typeof x === 'number' && Number.isFinite(x) && x >= 0;
const numeric = x => typeof x === 'string' && /^\d+$/.test(x) ? Number(x) : number(x) ? x : null;
function stats(values) {
  const v = values.filter(number), sum = v.length ? v.reduce((a, b) => a + b, 0) : null;
  return { observations: values.length, measured: v.length, missing: values.length - v.length, sum, mean: v.length ? sum / v.length : null, min: v.length ? Math.min(...v) : null, max: v.length ? Math.max(...v) : null };
}
const distribution = values => values.reduce((out, x) => { const key = x ?? 'unknown'; out[key] = (out[key] ?? 0) + 1; return out; }, {});
function memoryEvents(text) {
  if (typeof text !== 'string') return null;
  const pairs = text.trim().split('\n').map(line => line.trim().split(/\s+/));
  return pairs.length && pairs.every(([k, v]) => /^[a-z_]+$/.test(k) && /^\d+$/.test(v ?? '')) ? Object.fromEntries(pairs.map(([k, v]) => [k, Number(v)])) : null;
}
function resources(data, process) {
  const samples = Array.isArray(data.samples) ? data.samples : [];
  const fields = ['ram_available_bytes', 'model_rss_bytes', 'app_rss_bytes', 'gpu_free_mib', 'gpu_temperature_c'];
  return { target_sample_period_seconds: data.target_sample_period_seconds ?? null, sample_count: samples.length,
    sampling_error: data.sampling_error ?? null, sampling_error_field_present: Object.hasOwn(data, 'sampling_error'),
    missed_sample_intervals: data.missed_sample_intervals ?? null,
    sample_error_count: samples.length && samples.every(s => Array.isArray(s.errors)) ? samples.reduce((n, s) => n + s.errors.length, 0) : null,
    samples_missing_error_list: samples.filter(s => !Array.isArray(s.errors)).length,
    ac_states: distribution(samples.map(s => s.ac_online)),
    observed_interval_seconds: stats(samples.slice(1).map((s, i) => (Date.parse(s.at) - Date.parse(samples[i].at)) / 1000)),
    extrema: Object.fromEntries(fields.map(k => [k, stats(samples.map(s => s[k]))])),
    cgroups: Object.fromEntries(['model', 'app'].map(k => {
      const events = memoryEvents(process[`${k}_memory_events`]), state = process[k] ?? {};
      return [k, { memory_peak_bytes: numeric(state.MemoryPeak), memory_max_bytes: numeric(state.MemoryMax),
        memory_swap_max_bytes: numeric(state.MemorySwapMax), restarts: numeric(state.NRestarts), result: state.Result ?? null,
        memory_events: events, oom_zero_verified: events ? ['oom', 'oom_kill', 'oom_group_kill'].every(x => events[x] === 0) : null,
        restarts_zero_verified: numeric(state.NRestarts) === null ? null : numeric(state.NRestarts) === 0 }];
    })) };
}
function inference(d) {
  const present = Array.isArray(d.inference_summaries), entries = present ? d.inference_summaries : [];
  const seen = new Set();
  const attempts = entries.map(e => {
    const s = e.summary ?? {}, i = s.inference ?? {}, key = s.invocation_id;
    check(key && !seen.has(key), 'missing/duplicate invocation summary'); seen.add(key);
    const metric = k => { const m = i[k]; return i.source === 'llama.cpp' && m?.source === 'exact' && number(m.value) ? m.value : null; };
    const completed = s.invocation_status === 'completed' && i.status === 'completed';
    return { seq: e.seq ?? null, invocation_id: key, invocation_status: s.invocation_status ?? null, status: i.status ?? null,
      provider: i.source ?? null, completed, completed_output_tokens: completed ? metric('output_tokens') : null,
      prompt_tokens_per_second: metric('prompt_tokens_per_second'), output_tokens_per_second: metric('output_tokens_per_second'),
      metric_provenance: Object.fromEntries(['output_tokens', 'prompt_tokens_per_second', 'output_tokens_per_second'].map(k => [k, i[k] ?? null])) };
  });
  return { collection_present: present, collection_complete: d.inference_summaries_complete ?? null, attempts,
    invocation_statuses: distribution(attempts.map(a => a.invocation_status)), inference_statuses: distribution(attempts.map(a => a.status)),
    failed_attempts: present ? attempts.filter(a => a.invocation_status === 'failed').length : null,
    missing_output_telemetry_attempts: present ? attempts.filter(a => a.completed_output_tokens === null).length : null,
    known_completed_output_tokens_subtotal: stats(attempts.map(a => a.completed_output_tokens)),
    conditional_prompt_rate: stats(attempts.map(a => a.prompt_tokens_per_second)), conditional_generation_rate: stats(attempts.map(a => a.output_tokens_per_second)),
    total_actual_output_tokens_including_interrupted: null };
}

const rawProtocol = await bytes('protocol.json'), protocol = JSON.parse(rawProtocol), protocolHash = sha(rawProtocol);
const ledger = await json('accounting.final.json'); // Gate before output creation or CLI execution.
check(ledger.protocol_sha256 === protocolHash && Array.isArray(ledger.slots) && ledger.slots.length === 24 && ledger.slots.every(s => s.state === 'measured'), 'final ledger must contain all 24 measured slots with matching protocol hash');
const caseRaw = await readFile(resolve(root, 'internal/cli/testdata/lab-cases.json'));
const cases = JSON.parse(caseRaw).cases;
check(cases.length === 12 && new Set(cases.map(c => c.id)).size === 12 && cases.filter(c => c.split === 'development').length === 8 && cases.filter(c => c.split === 'heldout').length === 4 && cases.every(c => ['development', 'heldout'].includes(c.split)), 'invalid fixed manifest');
check(canonical(protocol.cases) === canonical(cases.map(c => c.id)) && sha(caseRaw) === protocol.dataset_sha256, 'case manifest differs from frozen protocol');
const actualHashes = { app_sha256: await digest(binary), llama_sha256: await digest(llama), gguf_sha256: await digest(gguf),
  corpus_sha256: await digest(resolve(root, 'internal/cli/testdata/lab-corpus.json')) };
for (const [k, v] of Object.entries(actualHashes)) check(v === protocol[k], `actual ${k} differs from protocol`);
const deployment = await json('deployment.json');
check(deployment.protocol_sha256 === protocolHash && deployment.operator_script_sha256 === await digest(resolve(dir, 'run.mjs')), 'deployment/operator provenance mismatch');
for (const [k, v] of Object.entries({ LLM_MODEL: protocol.model, LLM_CONTEXT_LENGTH: String(protocol.context), LLM_MAX_OUTPUT_TOKENS: String(protocol.output_tokens), LLM_TEMPERATURE: String(protocol.temperature), LLM_TOP_P: String(protocol.top_p), LLM_ENABLE_THINKING: String(protocol.thinking), LAB_HOLD_RESOURCE_PAUSES: 'true', CONTEXT_PROBE_ENABLED: 'false', RESEARCH_PRIVACY_MODE: 'offline' })) check(deployment.environment?.[k] === v, `deployment setting mismatch: ${k}`);
for (const [k, v] of Object.entries({ RESOURCE_GUARD_ENABLED: 'true', MODEL_RSS_MAX_BYTES: String(protocol.model_rss_guard_bytes), PROCESS_RSS_MAX_BYTES: String(protocol.app_rss_guard_bytes), RESOURCE_CHECK_INTERVAL_SECONDS: String(protocol.guard_interval_seconds), LLM_REQUEST_TIMEOUT_SECONDS: String(protocol.generation_timeout_seconds) })) check(deployment.environment?.[k] === v, `guard/timeout mismatch: ${k}`);
const ordered = cases.flatMap((c, i) => (i % 2 ? ['adaptive', 'baseline'] : ['baseline', 'adaptive']).map(variant => ({ ...c, variant })));
const rows = [], details = [], seenRuns = new Set(), seenSlots = new Set();
let common = null, commonPolicy = null;
for (const [i, expected] of ordered.entries()) {
  const slot = ledger.slots[i], key = `${expected.id}-${expected.variant}`;
  check(slot.case_id === expected.id && slot.variant === expected.variant && !seenSlots.has(key), `slot order/identity mismatch: ${i + 1}`); seenSlots.add(key);
  const prefix = `${String(i + 1).padStart(2, '0')}-${key}`;
  const lines = (await bytes(`${prefix}.jsonl`)).toString().trim().split('\n');
  check(lines.length === 1, `expected exactly one authoritative receipt: ${prefix}`);
  const r = JSON.parse(lines[0]), d = await json(`${prefix}-diagnostics.json`), start = await json(`${prefix}-start.json`), proc = await json(`${prefix}-process.json`), res = await json(`${prefix}-resources.json`);
  check(r.case_id === expected.id && r.variant === expected.variant && r.split === expected.split && r.run_id === slot.run_id && r.run_id && !seenRuns.has(r.run_id), `receipt identity mismatch: ${prefix}`); seenRuns.add(r.run_id);
  check(r.protocol_sha256 === protocolHash && r.experiment_id === protocolHash && r.dataset_sha256 === protocol.dataset_sha256 && r.corpus_sha256 === protocol.corpus_sha256, `receipt provenance mismatch: ${prefix}`);
  check(/^[a-f0-9]{64}$/.test(r.model_profile_hash) && r.budget && sha(JSON.stringify(r.budget)) === r.budget_hash, `model/budget hash invalid: ${prefix}`);
  const context = r.context_budget, runtime = d.runtime;
  check(context && context.context_status === 'matched' && ['configured_context', 'server_reported_context', 'session_context'].every(k => context[k] === protocol.context) && context.max_output_tokens === protocol.output_tokens && context.reserved_output_tokens === protocol.output_tokens, `context mismatch: ${prefix}`);
  const identity = { model: r.model_profile_hash, budget: r.budget_hash, fingerprint: context.runtime_fingerprint, template: context.chat_template_sha256 };
  check(identity.fingerprint && identity.template, `unknown runtime fingerprint: ${prefix}`);
  common ??= identity; check(canonical(identity) === canonical(common), `common runtime/budget drift: ${prefix}`);
  const policy = start.policy?.research_limits, adaptive = expected.variant === 'adaptive';
  check(policy && start.policy.hold_resource_pauses === true && policy.privacy_mode === 'offline' && policy.offline_corpus_sha256 === protocol.corpus_sha256 && policy.adaptive_enabled === adaptive && policy.sectioned_report_enabled === true, `start policy mismatch: ${prefix}`);
  const { adaptive_enabled, ...sharedPolicy } = policy;
  commonPolicy ??= sharedPolicy; check(canonical(sharedPolicy) === canonical(commonPolicy), `unallowed policy difference: ${prefix}`);
  check(runtime?.task_id === r.run_id && runtime.model_profile_hash === r.model_profile_hash && canonical(runtime.budget) === canonical(r.budget) && runtime.status === r.status && runtime.privacy_mode === 'offline' && runtime.offline_corpus_sha256 === protocol.corpus_sha256 && runtime.adaptive_enabled === adaptive && runtime.sectioned_report_enabled === true && runtime.max_continuations === (adaptive ? 2 : 1), `saved runtime policy mismatch: ${prefix}`);
  check(runtime.context_budget?.configured_context === protocol.context && runtime.context_budget?.max_output_tokens === protocol.output_tokens && runtime.context_budget?.runtime_fingerprint === identity.fingerprint, `diagnostic context drift: ${prefix}`);
  for (const k of ['model', 'app']) check(start[k]?.MemoryMax === String(protocol[`${k}_memory_max_bytes`]) && start[k]?.MemorySwapMax === '0' && start[k]?.Restart === 'no' && proc[k]?.MainPID === start[k]?.MainPID, `unit policy/PID drift: ${prefix}/${k}`);
  const evidence = Array.isArray(r.evidence) ? r.evidence : null;
  const urls = evidence ? [...new Set(evidence.flatMap(e => [e.url, e.original_url]).filter(Boolean))] : null;
  const matched = urls ? expected.source_urls.filter(u => urls.includes(u)) : null;
  const artifacts = Array.isArray(d.artifacts) ? d.artifacts : null;
  if (artifacts) for (const a of artifacts) check(a.task_id === r.run_id && /^[a-f0-9]{64}$/.test(a.sha256), `artifact ownership/hash invalid: ${prefix}`);
  const state = runtime.research_report ?? null, reportID = r.report_artifact_id || state?.artifact_id || null, markdownID = r.markdown_artifact_id || state?.markdown_artifact_id || null;
  const artifact = id => artifacts?.find(a => a.id === id) ?? null;
  const delivered = r.status === 'completed' && r.collection_complete === true && r.assistant_message_status === 'completed' && r.output_collected === true && r.output_bytes > 0;
  const reportDelivered = delivered && Boolean(reportID && markdownID && artifact(reportID) && artifact(markdownID));
  const metrics = { wall_seconds: r.wall_seconds ?? null, reservations: r.counters?.llm_calls ?? null, search: r.counters?.search_queries ?? null, fetch: r.counters?.fetches ?? null, evidence: r.counters?.evidence ?? null, compactions: r.compactions ?? null };
  rows.push(r);
  details.push({ slot: i + 1, case_id: r.case_id, split: r.split, variant: r.variant, run_id: r.run_id, receipt_file: `${prefix}.jsonl`, receipt: r,
    metrics, task_error_code: d.run?.error_code ?? null, complete_output: delivered, delivered_report: reportDelivered, delivered_sections: reportDelivered ? r.sections ?? null : null,
    in_progress_report_state: state, report_artifact: artifact(reportID), markdown_artifact: artifact(markdownID), artifacts,
    retrieval_source_recall: r.collection_complete && matched ? matched.length / expected.source_urls.length : null,
    source_info: { expected_urls: expected.source_urls, retrieved_urls: urls, matched_urls: matched, evidence, source: 'authoritative receipt task-owned evidence URLs; retrieval only, not answer support or independent accuracy' },
    saved_policy: policy, max_continuations: runtime.max_continuations, phases_observed: { current_round: runtime.current_round ?? null, completed_steps: runtime.completed_steps ?? null, research_stop_reason: runtime.research_stop_reason ?? null, rounds: runtime.research_rounds ?? null, completion: runtime.completion ?? null },
    resources: resources(res, proc), inference: inference(d), independent_factual_accuracy: null });
}
function aggregate(items) {
  const cgroups = Object.fromEntries(['model', 'app'].map(k => [k, {
    memory_peak_bytes: stats(items.map(x => x.resources.cgroups[k].memory_peak_bytes)), restarts: stats(items.map(x => x.resources.cgroups[k].restarts)),
    memory_events: Object.fromEntries(['low', 'high', 'max', 'oom', 'oom_kill', 'oom_group_kill'].map(e => [e, stats(items.map(x => x.resources.cgroups[k].memory_events?.[e]))])),
    all_oom_zero_verified: items.every(x => x.resources.cgroups[k].oom_zero_verified === true), all_restarts_zero_verified: items.every(x => x.resources.cgroups[k].restarts_zero_verified === true) }]));
  return { slots: items.length, complete_outputs: items.filter(x => x.complete_output).length, delivered_reports: items.filter(x => x.delivered_report).length,
    paused: items.filter(x => x.receipt.status === 'paused').length, successful_outcomes: items.filter(x => x.delivered_report && x.receipt.outcome === 'completed' && x.receipt.report_outcome === 'completed').length, statuses: distribution(items.map(x => x.receipt.status)),
    resource_reasons: distribution(items.map(x => x.receipt.resource_reason || 'none')), metrics: Object.fromEntries(Object.keys(items[0].metrics).map(k => [k, stats(items.map(x => x.metrics[k]))])),
    retrieval_source_recall: stats(items.map(x => x.retrieval_source_recall)),
    sampled_resources: Object.fromEntries(Object.keys(items[0].resources.extrema).map(k => [k, { min: stats(items.map(x => x.resources.extrema[k].min)).min, max: stats(items.map(x => x.resources.extrema[k].max)).max, measured_samples: items.reduce((n, x) => n + x.resources.extrema[k].measured, 0) }])),
    sampling_errors: items.filter(x => x.resources.sampling_error !== null).map(x => ({ slot: x.slot, error: x.resources.sampling_error })),
    sample_errors: stats(items.map(x => x.resources.sample_error_count)), cgroups,
    inference_summary_collection_incomplete_slots: items.filter(x => x.inference.collection_complete !== true).map(x => x.slot),
    failed_inference_attempts: stats(items.map(x => x.inference.failed_attempts)), missing_output_telemetry_attempts: stats(items.map(x => x.inference.missing_output_telemetry_attempts)),
    known_completed_output_tokens_subtotal: stats(items.flatMap(x => x.inference.attempts.map(a => a.completed_output_tokens))),
    conditional_prompt_rate: stats(items.flatMap(x => x.inference.attempts.map(a => a.prompt_tokens_per_second))),
    conditional_generation_rate: stats(items.flatMap(x => x.inference.attempts.map(a => a.output_tokens_per_second))), independent_factual_accuracy: null };
}
// All inputs checked before creating analytical outputs; no overwrite or implicit rerun.
for (const name of ['baseline.jsonl', 'adaptive.jsonl', 'comparison.json', 'summary.json']) {
  try { await lstat(resolve(dir, name)); throw Error(`output already exists: ${name}`); } catch (e) { if (e.code !== 'ENOENT') throw e; }
}
for (const variant of ['baseline', 'adaptive']) await writeFile(resolve(dir, `${variant}.jsonl`), cases.map(c => JSON.stringify(rows.find(r => r.case_id === c.id && r.variant === variant))).join('\n') + '\n', { flag: 'wx', mode: 0o600 });
const compared = await new Promise(resolveResult => {
  const child = spawn(binary, ['lab', 'compare', resolve(dir, 'baseline.jsonl'), resolve(dir, 'adaptive.jsonl')], { cwd: root, stdio: ['ignore', 'pipe', 'pipe'] });
  let stdout = '', stderr = '', error = null;
  const timer = setTimeout(() => { error = 'compare timeout'; child.kill('SIGKILL'); }, 60000);
  child.stdout.on('data', b => { stdout += b; if (stdout.length > 8 << 20) { error = 'compare output bound exceeded'; child.kill('SIGKILL'); } });
  child.stderr.on('data', b => { stderr += b; if (stderr.length > 8 << 20) { error = 'compare error bound exceeded'; child.kill('SIGKILL'); } });
  child.on('error', e => { error = String(e); });
  child.on('close', (exit_code, signal) => { clearTimeout(timer); resolveResult({ exit_code, signal, stdout, stderr, error }); });
});
let comparison = null;
try { if (compared.exit_code === 0 && !compared.error) comparison = JSON.parse(compared.stdout); } catch (e) { compared.error = String(e); }
await write('comparison.json', comparison ?? { comparison_failed: true, ...compared });
await write('summary.json', { type: 'phase3_offline_analysis', version: 1, protocol_sha256: protocolHash, generated_at: new Date().toISOString(),
  provenance: { actual_hashes_verified_at_analysis: actualHashes, declared_protocol: protocol, operator_script_sha256: deployment.operator_script_sha256, common_receipt_identity: common, common_safe_policy: commonPolicy, historical_hash_attestation_limit: 'Current file hashes and captured launch/config records; not retroactive file-content attestation for every process.' },
  accounting: ledger, comparison_succeeded: comparison !== null, comparison_execution: compared,
  interpretation: 'One attempt per slot; first-pause resource censoring compares computational cost to interruption, not quality wins. Conditional provider rates and known completed-call token subtotals exclude unknown/interrupted usage. Checkpoint section counts are not delivered reports. Very limited observed phases do not establish adaptive effectiveness.',
  all_slots: details, overall: aggregate(details), by_variant_and_split: Object.fromEntries(['baseline', 'adaptive'].map(v => [v, Object.fromEntries(['all', 'development', 'heldout'].map(s => [s, aggregate(details.filter(x => x.variant === v && (s === 'all' || x.split === s)))]))])),
  independent_factual_accuracy: null, total_actual_output_tokens_including_interrupted: null });
if (!comparison) process.exitCode = 1;
