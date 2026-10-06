#!/usr/bin/env python3

import argparse
import json
import re
import subprocess
from collections import Counter, defaultdict
from datetime import datetime
from pathlib import Path
from statistics import mean

INPUT_USD_PER_M = 3.0
OUTPUT_USD_PER_M = 15.0
TERMINAL = {"recovered", "exhausted", "escalated", "rejected"}
ACTIONABLE_TERMINAL = {"recovered", "exhausted"}

RUN_PREFIXES = {
    "A1": ("W1", "enabled", 5),
    "A2": ("W1", "disabled", 5),
    "B1": ("W2", "enabled", 5),
    "B2": ("W2", "disabled", 5),
    "C1": ("W3", "enabled", 5),
    "C2": ("W3", "disabled", 3),
}
EXPECTED_RUN_IDS = [
    f"{prefix}-{i:02d}"
    for prefix, (_, _, count) in RUN_PREFIXES.items()
    for i in range(1, count + 1)
]
EXPECTED_RUN_TOTAL = len(EXPECTED_RUN_IDS)

VALIDATOR_RUN_RE = (
    r"^(TestWeek3AdversarialRejectionRate|"
    r"TestWeek3LegitimateAcceptanceRate)$"
)
VALIDATOR_COMMAND = [
    "go",
    "test",
    "./internal/classifier",
    "-run",
    VALIDATOR_RUN_RE,
    "-count=1",
    "-v",
]
VALIDATOR_SOURCE_COMMAND = (
    "go test ./internal/classifier "
    f"-run '{VALIDATOR_RUN_RE}' -count=1 -v"
)


def fail(msg):
    raise SystemExit("ERROR: " + msg)


def require(condition, msg):
    if not condition:
        fail(msg)


def is_int(value):
    return type(value) is int


def load_json(path):
    if not path.exists():
        fail(f"{path}: missing required JSON file")
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except Exception as e:
        fail(f"{path}: {e}")


def load_jsonl(path):
    if not path.exists():
        return []
    out = []
    for n, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        if not line.strip():
            continue
        try:
            out.append(json.loads(line))
        except Exception as e:
            fail(f"{path}:{n}: {e}")
    return out


def ts(value):
    if not value:
        fail("missing timestamp")
    return datetime.fromisoformat(value.replace("Z", "+00:00"))


def seconds(a, b):
    return (ts(b) - ts(a)).total_seconds()


def avg(values):
    return mean(values) if values else None


def cost(inp, out):
    return inp * INPUT_USD_PER_M / 1_000_000 + out * OUTPUT_USD_PER_M / 1_000_000


def classifier_record(run_id, row):
    inp = row.get("inputTokens")
    out = row.get("outputTokens")
    total = row.get("totalTokens")
    millis = row.get("classifierMillis")

    if not is_int(inp) or inp <= 0:
        fail(f"{run_id}: invalid classifier inputTokens")
    if not is_int(out) or out <= 0:
        fail(f"{run_id}: invalid classifier outputTokens")
    if not is_int(total) or total != inp + out:
        fail(f"{run_id}: invalid classifier totalTokens")
    if not is_int(millis) or millis <= 0:
        fail(f"{run_id}: invalid classifierMillis")

    cost_known = row.get("costKnown") is True
    if cost_known:
        estimated_cost = row.get("estimatedCostUSD")
        if not isinstance(estimated_cost, (int, float)) or estimated_cost < 0:
            fail(f"{run_id}: invalid estimatedCostUSD")
        estimated_cost = float(estimated_cost)
        cost_source = "recorded"
    else:
        estimated_cost = cost(inp, out)
        cost_source = "backfilled_from_tokens"

    return {
        "runID": run_id,
        "provider": row.get("classifierProvider"),
        "model": row.get("classifierModel"),
        "millis": millis,
        "inputTokens": inp,
        "outputTokens": out,
        "totalTokens": total,
        "costUSD": estimated_cost,
        "costSource": cost_source,
    }


def expected_identity(run_id):
    parts = run_id.split("-", 1)
    if len(parts) != 2:
        fail(f"{run_id}: malformed runID")

    prefix, suffix = parts
    if prefix not in RUN_PREFIXES:
        fail(f"{run_id}: unexpected historical run prefix")
    if not re.fullmatch(r"\d{2}", suffix):
        fail(f"{run_id}: malformed run suffix")

    workload, arm, count = RUN_PREFIXES[prefix]
    ordinal = int(suffix)
    if ordinal < 1 or ordinal > count:
        fail(f"{run_id}: run suffix outside expected range for {prefix}")

    return workload, arm


def validate_matrix(matrix):
    require(isinstance(matrix, list), "matrix must be a JSON array")
    require(
        len(matrix) == EXPECTED_RUN_TOTAL,
        f"matrix must contain {EXPECTED_RUN_TOTAL} runs, found {len(matrix)}",
    )

    seen = set()
    run_ids = []

    for i, spec in enumerate(matrix, 1):
        require(isinstance(spec, dict), f"matrix row {i}: expected object")
        run_id = spec.get("runID")
        require(isinstance(run_id, str) and run_id, f"matrix row {i}: missing runID")
        if run_id in seen:
            fail(f"duplicate runID in matrix: {run_id}")
        seen.add(run_id)
        run_ids.append(run_id)

        workload, arm = expected_identity(run_id)
        if "workload" in spec and spec["workload"] != workload:
            fail(f"{run_id}: matrix workload mismatch")
        if "arm" in spec and spec["arm"] != arm:
            fail(f"{run_id}: matrix arm mismatch")

    expected = sorted(EXPECTED_RUN_IDS)
    actual = sorted(run_ids)
    if actual != expected:
        fail(
            "matrix run IDs do not exactly match historical dataset\n"
            f"missing={sorted(set(expected)-set(actual))}\n"
            f"extra={sorted(set(actual)-set(expected))}"
        )


def validate_meta(run_id, meta, workload, arm):
    require(isinstance(meta, dict), f"{run_id}: meta.json must contain an object")
    require(meta.get("runID") == run_id, f"{run_id}: runID mismatch")

    if "workload" in meta and meta["workload"] != workload:
        fail(f"{run_id}: workload mismatch")
    if "arm" in meta and meta["arm"] != arm:
        fail(f"{run_id}: arm mismatch")

    require(
        meta.get("injectionSucceeded") is True,
        f"{run_id}: injection did not succeed",
    )
    require(meta.get("injectionExitCode") == 0, f"{run_id}: injectionExitCode != 0")

    if arm == "enabled" and not isinstance(meta.get("injectedAt"), str):
        fail(f"{run_id}: missing injectedAt")
    if "injectedAt" in meta:
        ts(meta["injectedAt"])

    if "recovered" in meta and not isinstance(meta["recovered"], bool):
        fail(f"{run_id}: meta recovered must be boolean")

    if "observationCutoffSeconds" in meta:
        cutoff = meta["observationCutoffSeconds"]
        if not is_int(cutoff) or cutoff <= 0:
            fail(f"{run_id}: invalid observationCutoffSeconds")


def validate_audit(run_id, audit):
    require(isinstance(audit, list), f"{run_id}: audit must be JSONL objects")

    for i, row in enumerate(audit, 1):
        require(isinstance(row, dict), f"{run_id}: audit line {i} must be object")

        for field in ("timestamp", "pod", "state", "result"):
            require(
                isinstance(row.get(field), str) and row[field],
                f"{run_id}: audit line {i} missing {field}",
            )
        require(
            isinstance(row.get("action"), str),
            f"{run_id}: audit line {i} missing action",
        )

        ts(row["timestamp"])

        incident_id = row.get("incidentID")
        require(
            isinstance(incident_id, str) and incident_id,
            f"{run_id}: audit line {i} missing incidentID",
        )

        attempt = row.get("attemptNumber")
        require(
            is_int(attempt) and attempt >= 0,
            f"{run_id}: audit line {i} has invalid attemptNumber",
        )


def observed_recovery(run_id, path):
    if not path.exists():
        return None, None

    value = path.read_text(encoding="utf-8").strip()
    if value == "not_recovered_within_300s":
        return False, value
    if value == "recovered":
        return True, value

    fail(f"{run_id}: unknown observed result {value!r}")


def experiment_recovery(run_id, arm, meta, audit, observed_path):
    meta_recovered = meta.get("recovered") if "recovered" in meta else None
    observed, observed_source = observed_recovery(run_id, observed_path)

    if meta_recovered is not None and observed is not None and meta_recovered != observed:
        fail(f"{run_id}: meta recovered disagrees with observed-result.txt")

    if meta_recovered is not None:
        return meta_recovered, meta.get("recoverySource") or "meta:recovered"

    if observed is not None:
        return observed, observed_source

    if arm == "disabled":
        fail(f"{run_id}: disabled run missing recovery observation")

    closed = [x for x in audit if x.get("state") == "CLOSED"]
    if len(closed) != 1:
        fail(f"{run_id}: enabled run missing experiment recovery evidence")

    result = closed[0].get("result")
    if result not in TERMINAL:
        fail(f"{run_id}: invalid CLOSED result {result!r}")

    return result == "recovered", f"closed:{result}"


def parse_validator_result(output, label, expected_total):
    pattern = re.compile(
        rf"Week-3 {label}: ([0-9]+)/([0-9]+) = ([0-9]+(?:\.[0-9]+)?)%"
    )
    matches = pattern.findall(output)
    if len(matches) != 1:
        fail(f"validator output missing unique Week-3 {label} result")

    numerator, denominator, rate = matches[0]
    numerator = int(numerator)
    denominator = int(denominator)
    rate = float(rate)

    if denominator != expected_total:
        fail(
            f"validator {label} expected denominator {expected_total}, "
            f"got {denominator}"
        )

    derived = numerator / denominator * 100.0 if denominator else 0.0
    if abs(derived - rate) > 0.01:
        fail(f"validator {label} rate disagrees with numerator/denominator")

    return numerator, denominator, derived


def validator_evaluation():
    proc = subprocess.run(
        VALIDATOR_COMMAND,
        cwd=Path.cwd(),
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        check=False,
    )

    if proc.returncode != 0:
        fail(
            "validator go test failed\n"
            f"command={' '.join(VALIDATOR_COMMAND)}\n"
            f"output:\n{proc.stdout}"
        )

    adv_rejected, adv_total, adv_rate = parse_validator_result(
        proc.stdout,
        "adversarial rejection",
        15,
    )
    legit_accepted, legit_total, legit_rate = parse_validator_result(
        proc.stdout,
        "legitimate acceptance",
        30,
    )

    return {
        "sourceCommand": VALIDATOR_SOURCE_COMMAND,
        "adversarialRejected": adv_rejected,
        "adversarialTotal": adv_total,
        "adversarialRejectionRatePercent": adv_rate,
        "legitimateAccepted": legit_accepted,
        "legitimateTotal": legit_total,
        "legitimateAcceptanceRatePercent": legit_rate,
        "falseAcceptRatePercent": (
            (adv_total - adv_rejected) / adv_total * 100.0
            if adv_total else 0.0
        ),
        "falseRejectRatePercent": (
            (legit_total - legit_accepted) / legit_total * 100.0
            if legit_total else 0.0
        ),
    }


def expect_equal(label, actual, expected):
    if actual != expected:
        fail(f"{label}: expected {expected!r}, got {actual!r}")


def expect_close(label, actual, expected, tolerance=0.000001):
    if actual is None or abs(actual - expected) > tolerance:
        fail(f"{label}: expected {expected!r}, got {actual!r}")


def validate_validator_contract(validator_summary):
    expect_equal("validator adversarialRejected", validator_summary["adversarialRejected"], 15)
    expect_equal("validator adversarialTotal", validator_summary["adversarialTotal"], 15)
    expect_close(
        "validator adversarialRejectionRatePercent",
        validator_summary["adversarialRejectionRatePercent"],
        100.0,
    )
    expect_equal("validator legitimateAccepted", validator_summary["legitimateAccepted"], 30)
    expect_equal("validator legitimateTotal", validator_summary["legitimateTotal"], 30)
    expect_close(
        "validator legitimateAcceptanceRatePercent",
        validator_summary["legitimateAcceptanceRatePercent"],
        100.0,
    )
    expect_close(
        "validator falseAcceptRatePercent",
        validator_summary["falseAcceptRatePercent"],
        0.0,
    )
    expect_close(
        "validator falseRejectRatePercent",
        validator_summary["falseRejectRatePercent"],
        0.0,
    )


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--runs-dir", default="sage-final-runs")
    ap.add_argument("--matrix", default="hack/experiment/week3-matrix.json")
    ap.add_argument("--json-out", default="week3-live-final-results.json")
    ap.add_argument("--md-out", default="docs/week3-live-final-results.md")
    args = ap.parse_args()

    root = Path(args.runs_dir)
    matrix = load_json(Path(args.matrix))
    validate_matrix(matrix)

    matrix_by_id = {x["runID"]: x for x in matrix}

    require(root.exists() and root.is_dir(), f"{root}: missing runs directory")

    actual = sorted(x.name for x in root.iterdir() if x.is_dir())
    expected = sorted(EXPECTED_RUN_IDS)

    if actual != expected:
        fail(
            "run directories do not exactly match historical dataset\n"
            f"missing={sorted(set(expected)-set(actual))}\n"
            f"extra={sorted(set(actual)-set(expected))}"
        )

    run_rows = []
    classifier_calls = []

    attempt_count = 0
    rolled_back_attempts = 0
    actionable_incidents = set()
    incidents_with_rollback = set()
    abandoned_incidents = set()
    incident_terminal_results = {}
    terminal_outcomes = Counter()

    ttd = []
    ttm = []
    classify_times = []
    apply_times = []
    verify_times = []

    for rid in EXPECTED_RUN_IDS:
        spec = matrix_by_id[rid]
        workload, arm = expected_identity(rid)
        d = root / rid

        meta = load_json(d / "meta.json")
        validate_meta(rid, meta, workload, arm)

        if "workload" in spec and spec["workload"] != workload:
            fail(f"{rid}: matrix workload mismatch")
        if "arm" in spec and spec["arm"] != arm:
            fail(f"{rid}: matrix arm mismatch")

        audit_path = d / "audit.jsonl"
        audit = load_jsonl(audit_path)
        validate_audit(rid, audit)

        if arm == "enabled":
            if not audit_path.exists() or not audit:
                fail(f"{rid}: enabled run missing controller audit")
        elif audit:
            fail(f"{rid}: disabled run has controller audit rows")

        for row in audit:
            row["_runID"] = rid

        # ----------------------------
        # Experiment recovery outcome
        # ----------------------------
        recovered, source = experiment_recovery(
            rid,
            arm,
            meta,
            audit,
            d / "observed-result.txt",
        )

        # ----------------------------
        # Incident/attempt accounting
        # ----------------------------
        incident_ids = {
            x.get("incidentID")
            for x in audit
            if x.get("incidentID")
        }

        actionable_incidents.update(incident_ids)

        remediating = [
            x for x in audit
            if x.get("state") == "REMEDIATING"
        ]
        attempt_count += len(remediating)

        rolled = [
            x for x in audit
            if x.get("state") == "ROLLED_BACK"
        ]
        rolled_back_attempts += len(rolled)
        incidents_with_rollback.update(
            x["incidentID"] for x in rolled if x.get("incidentID")
        )

        closed = [x for x in audit if x.get("state") == "CLOSED"]

        for x in closed:
            result = x.get("result")
            if result not in TERMINAL:
                fail(f"{rid}: invalid terminal result {result!r}")
            iid = x.get("incidentID")
            if iid in incident_terminal_results:
                fail(f"{rid}: duplicate CLOSED record for incident {iid}")
            incident_terminal_results[iid] = result
            terminal_outcomes[result] += 1

        for iid in incident_ids:
            rows = [x for x in audit if x.get("incidentID") == iid]
            had_attempt = any(x.get("state") == "REMEDIATING" for x in rows)
            had_closed = any(x.get("state") == "CLOSED" for x in rows)
            if had_attempt and not had_closed:
                abandoned_incidents.add(iid)

        # ----------------------------
        # Timing
        # ----------------------------
        injected = meta.get("injectedAt")

        for iid in incident_ids:
            rows = sorted(
                [x for x in audit if x.get("incidentID") == iid],
                key=lambda x: ts(x["timestamp"])
            )

            detected = next(
                (x for x in rows if x.get("state") == "DETECTED"),
                None
            )

            if detected and injected:
                ttd.append(seconds(injected, detected["timestamp"]))

            closed_row = next(
                (x for x in rows if x.get("state") == "CLOSED"),
                None
            )

            if detected and closed_row:
                ttm.append(
                    seconds(detected["timestamp"], closed_row["timestamp"])
                )

        # attempt-level apply/verify
        attempts = defaultdict(list)
        for x in audit:
            iid = x.get("incidentID")
            num = x.get("attemptNumber")
            if iid and isinstance(num, int) and num > 0:
                attempts[(iid, num)].append(x)

        for rows in attempts.values():
            rem = next(
                (x for x in rows if x.get("state") == "REMEDIATING"),
                None
            )
            ver = next(
                (x for x in rows if x.get("state") == "VERIFYING"),
                None
            )

            if rem and ver:
                apply_times.append(
                    seconds(rem["timestamp"], ver["timestamp"])
                )

            if ver:
                end = next(
                    (
                        x for x in rows
                        if x.get("state") in {"RECOVERED", "ROLLING_BACK"}
                    ),
                    None
                )
                if end:
                    verify_times.append(
                        seconds(ver["timestamp"], end["timestamp"])
                    )

        # ----------------------------
        # Classifier accounting
        # ----------------------------
        call_file = d / "classifier-calls.jsonl"
        calls = load_jsonl(call_file)

        # New A1 recorder contains independent per-call records.
        if calls:
            for c in calls:
                record = classifier_record(rid, c)
                classifier_calls.append(record)
                classify_times.append(record["millis"] / 1000.0)
        else:
            # Older B/C live runs persist classifier metadata on CLOSED.
            for c in closed:
                inp = c.get("inputTokens")
                out = c.get("outputTokens")
                millis = c.get("classifierMillis")

                if inp is None and out is None and millis is None:
                    continue

                record = classifier_record(rid, c)
                classifier_calls.append(record)
                classify_times.append(record["millis"] / 1000.0)

        run_rows.append({
            "runID": rid,
            "workload": workload,
            "arm": arm,
            "recovered": recovered,
            "recoverySource": source,
        })

    # ----------------------------
    # Experiment recovery matrix
    # ----------------------------
    recovery = {}

    for workload in ("W1", "W2", "W3"):
        recovery[workload] = {}

        for arm in ("enabled", "disabled"):
            rows = [
                x for x in run_rows
                if x["workload"] == workload and x["arm"] == arm
            ]

            if not rows:
                fail(f"{workload}/{arm}: no runs")

            successes = sum(1 for x in rows if x["recovered"])
            total = len(rows)

            recovery[workload][arm] = {
                "recovered": successes,
                "total": total,
                "ratePercent": successes / total * 100.0,
            }

        recovery[workload]["attributableRecoveryPercentagePoints"] = (
            recovery[workload]["enabled"]["ratePercent"]
            - recovery[workload]["disabled"]["ratePercent"]
        )

    terminal_total = sum(terminal_outcomes.values())

    rollback_attempt_rate = (
        rolled_back_attempts / attempt_count * 100.0
        if attempt_count else 0.0
    )

    # Incident-level rollback denominator is actionable terminal incidents only.
    # Abandoned incidents have no terminal outcome and are excluded.
    actionable_terminal_incidents = (
        terminal_outcomes["recovered"]
        + terminal_outcomes["exhausted"]
    )

    terminal_incidents_with_rollback = 0
    for iid in incidents_with_rollback:
        if incident_terminal_results.get(iid) in ACTIONABLE_TERMINAL:
            terminal_incidents_with_rollback += 1

    rollback_incident_rate = (
        terminal_incidents_with_rollback
        / actionable_terminal_incidents
        * 100.0
        if actionable_terminal_incidents else 0.0
    )

    # Five frozen terminal outcome buckets.
    outcome_counts = {
        "recovered": terminal_outcomes["recovered"],
        "exhausted": terminal_outcomes["exhausted"],
        "escalated": terminal_outcomes["escalated"],
        "rejected": terminal_outcomes["rejected"],
        "abandoned": len(abandoned_incidents),
    }

    classifier_summary = {
        "persistedCallRecords": len(classifier_calls),
        "averageLatencySeconds": avg([
            x["millis"] / 1000.0 for x in classifier_calls
        ]),
        "totalInputTokens": sum(x["inputTokens"] for x in classifier_calls),
        "totalOutputTokens": sum(x["outputTokens"] for x in classifier_calls),
        "totalTokens": sum(x["totalTokens"] for x in classifier_calls),
        "totalEstimatedCostUSD": sum(x["costUSD"] for x in classifier_calls),
        "pricing": {
            "model": "claude-sonnet-4-6",
            "inputPerMillionUSD": INPUT_USD_PER_M,
            "outputPerMillionUSD": OUTPUT_USD_PER_M,
        },
        "calls": classifier_calls,
        "note": (
            "A1 calls come from classifier-calls.jsonl. "
            "Older B/C artifacts persist classifier metadata on CLOSED; "
            "their costs are backfilled deterministically from stored token counts. "
            "For C1, the older CLOSED-only instrumentation preserves the terminal "
            "classifier call rather than every attempt-level call. "
            "Represented classifier cost is not necessarily total actual "
            "experiment cost."
        ),
    }

    validator_summary = validator_evaluation()

    controller_summary = {
        "incidents": len(actionable_incidents),
        "terminalIncidents": terminal_total,
        "abandonedIncidents": len(abandoned_incidents),
        "remediationAttempts": attempt_count,
        "rolledBackAttempts": rolled_back_attempts,
        "incidentsWithRollback": len(incidents_with_rollback),
        "rollbackAttemptRatePercent": rollback_attempt_rate,
        "rollbackIncidentRatePercent": rollback_incident_rate,
        "terminalOutcomes": outcome_counts,
    }

    timing_summary = {
        "averageTTD": avg(ttd),
        "averageTTM": avg(ttm),
        "averageClassifier": avg(classify_times),
        "averageApply": avg(apply_times),
        "averageVerify": avg(verify_times),
    }

    validate_validator_contract(validator_summary)

    summary = {
        "dataset": {
            "runs": len(run_rows),
            "matrixRuns": EXPECTED_RUN_TOTAL,
            "valid": True,
        },
        "experimentRecovery": recovery,
        "controller": controller_summary,
        "timingSeconds": timing_summary,
        "classifier": classifier_summary,
        "validatorEvaluation": validator_summary,
    }

    json_out = Path(args.json_out)
    json_out.write_text(
        json.dumps(summary, indent=2) + "\n",
        encoding="utf-8"
    )

    md = []
    md.append("# SAGE-K8s Week 3 Live Final Results")
    md.append("")
    md.append("## Dataset")
    md.append("")
    md.append(f"- Valid runs: **{len(run_rows)}/{EXPECTED_RUN_TOTAL}**")
    md.append("")

    md.append("## Experiment recovery")
    md.append("")
    md.append("| Workload | Enabled | Disabled | Attributable recovery |")
    md.append("|---|---:|---:|---:|")

    for w in ("W1", "W2", "W3"):
        e = recovery[w]["enabled"]
        d = recovery[w]["disabled"]
        a = recovery[w]["attributableRecoveryPercentagePoints"]
        md.append(
            f"| {w} | {e['recovered']}/{e['total']} "
            f"({e['ratePercent']:.1f}%) | "
            f"{d['recovered']}/{d['total']} "
            f"({d['ratePercent']:.1f}%) | "
            f"{a:+.1f} pp |"
        )

    md.append("")
    md.append("## Controller outcomes")
    md.append("")
    md.append(f"- Incidents: **{len(actionable_incidents)}**")
    md.append(f"- Terminal incidents: **{terminal_total}**")
    md.append(f"- Abandoned incidents: **{len(abandoned_incidents)}**")
    md.append(f"- Remediation attempts: **{attempt_count}**")
    md.append(f"- Rolled-back attempts: **{rolled_back_attempts}**")
    md.append(f"- Rollback attempt rate: **{rollback_attempt_rate:.1f}%**")
    md.append(f"- Rollback incident rate: **{rollback_incident_rate:.1f}%**")
    md.append("")

    md.append("## Terminal outcomes")
    md.append("")
    for name in ("recovered", "exhausted", "escalated", "rejected", "abandoned"):
        md.append(f"- {name}: **{outcome_counts[name]}**")

    md.append("")
    md.append("## Timing")
    md.append("")
    md.append(f"- Average TTD: **{avg(ttd):.3f} s**")
    md.append(f"- Average TTM: **{avg(ttm):.3f} s**")
    md.append(f"- Average classifier latency: **{avg(classify_times):.3f} s**")
    md.append(f"- Average apply: **{avg(apply_times):.6f} s**")
    md.append(f"- Average verify: **{avg(verify_times):.3f} s**")
    md.append("")

    md.append("## Classifier")
    md.append("")
    md.append(
        f"- Persisted classifier call records represented: "
        f"**{len(classifier_calls)}**"
    )
    md.append(
        f"- Input tokens represented: "
        f"**{classifier_summary['totalInputTokens']}**"
    )
    md.append(
        f"- Output tokens represented: "
        f"**{classifier_summary['totalOutputTokens']}**"
    )
    md.append(
        f"- Total tokens represented: "
        f"**{classifier_summary['totalTokens']}**"
    )
    md.append(
        f"- Estimated cost represented: "
        f"**${classifier_summary['totalEstimatedCostUSD']:.6f}**"
    )
    md.append("")
    md.append(
        "> Note: A1 has independent per-call recorder data. "
        "Older B/C runs store classifier metadata on CLOSED. "
        "Therefore C1 preserves the terminal classifier call, not all "
        "three attempt-level calls. Cost figures are reported only for "
        "persisted call records and are not extrapolated; represented "
        "classifier cost is not necessarily total actual experiment cost."
    )
    md.append("")

    md.append("## Validator evaluation")
    md.append("")
    md.append(f"- Source: `{validator_summary['sourceCommand']}`")
    md.append("")
    md.append("| Metric | Result |")
    md.append("|---|---:|")
    md.append(
        "| Adversarial rejection | "
        f"{validator_summary['adversarialRejected']}/"
        f"{validator_summary['adversarialTotal']} "
        f"({validator_summary['adversarialRejectionRatePercent']:.1f}%) |"
    )
    md.append(
        "| Legitimate acceptance | "
        f"{validator_summary['legitimateAccepted']}/"
        f"{validator_summary['legitimateTotal']} "
        f"({validator_summary['legitimateAcceptanceRatePercent']:.1f}%) |"
    )
    md.append(
        "| False accept rate | "
        f"{validator_summary['falseAcceptRatePercent']:.1f}% |"
    )
    md.append(
        "| False reject rate | "
        f"{validator_summary['falseRejectRatePercent']:.1f}% |"
    )
    md.append("")

    md_out = Path(args.md_out)
    md_out.parent.mkdir(parents=True, exist_ok=True)
    md_out.write_text("\n".join(md), encoding="utf-8")

    print("===== FINAL WEEK 3 RESULTS =====")
    print(f"runs: {len(run_rows)}/{EXPECTED_RUN_TOTAL}")

    for w in ("W1", "W2", "W3"):
        e = recovery[w]["enabled"]
        d = recovery[w]["disabled"]
        a = recovery[w]["attributableRecoveryPercentagePoints"]
        print(
            f"{w}: enabled={e['ratePercent']:.1f}% "
            f"disabled={d['ratePercent']:.1f}% "
            f"attributable={a:+.1f}pp"
        )

    print(f"incidents: {len(actionable_incidents)}")
    print(f"terminal incidents: {terminal_total}")
    print(f"abandoned: {len(abandoned_incidents)}")
    print(f"attempts: {attempt_count}")
    print(f"rolled-back attempts: {rolled_back_attempts}")
    print(f"rollback attempt rate: {rollback_attempt_rate:.1f}%")
    print(f"rollback incident rate: {rollback_incident_rate:.1f}%")
    print(f"average TTD: {avg(ttd):.3f}s")
    print(f"average TTM: {avg(ttm):.3f}s")
    print(f"classifier records represented: {len(classifier_calls)}")
    print(
        "represented classifier cost: "
        f"${classifier_summary['totalEstimatedCostUSD']:.6f}"
    )
    print(
        "validator adversarial rejection: "
        f"{validator_summary['adversarialRejected']}/"
        f"{validator_summary['adversarialTotal']} "
        f"({validator_summary['adversarialRejectionRatePercent']:.1f}%)"
    )
    print(
        "validator legitimate acceptance: "
        f"{validator_summary['legitimateAccepted']}/"
        f"{validator_summary['legitimateTotal']} "
        f"({validator_summary['legitimateAcceptanceRatePercent']:.1f}%)"
    )
    print(
        "validator false accept rate: "
        f"{validator_summary['falseAcceptRatePercent']:.1f}%"
    )
    print(
        "validator false reject rate: "
        f"{validator_summary['falseRejectRatePercent']:.1f}%"
    )
    print(f"JSON: {json_out}")
    print(f"Markdown: {md_out}")


if __name__ == "__main__":
    main()
