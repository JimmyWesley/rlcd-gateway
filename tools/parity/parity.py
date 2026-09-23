#!/usr/bin/env python3
"""open-rlcd vs Jev parity check on external decision cases.

Runs dataset.json through an RLCD Gateway's System One proxy and reports,
per category: agreement between the two backends, accuracy against the
expected answers (where the dataset gives one), mean confidence, and
latency p50/p95. Python 3 standard library only; no keys in this file.

Three modes:

  mirror (default)  one call per case with the primary model; the gateway's
                    mirror sends a copy to the second backend and the script
                    reads both answers back from GET /api/decisions. The
                    gateway needs, in its "decisions" section, backends for
                    both models and
                    "mirror": {"backend": "jev", "sample_rate": 1, "model": "jev-latest"}.
  both              two calls per case through the gateway, one per model.
  primary           the primary model only (no secondary calls at all).

The gateway mirrors in the background with 8 slots and skips a copy when
they are all busy; --pace-ms spaces the calls so a fast primary does not
outrun a slower mirror.

Score answers are the rounded expected score (what the gateway's decision
rules and audit use). When the primary returns probabilities, the report
also gives its accuracy with the most probable index instead.

    python3 tools/parity/parity.py --gateway http://127.0.0.1:4830 \\
        --primary Open-RLCD-text --secondary jev-latest --out results.json

Latencies are the gateway's own end-to-end timings of each backend call
(network included), so they compare the deployments, not only the models.
"""

import argparse
import json
import math
import os
import sys
import time
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))


def http(method, url, body=None, headers=None, timeout=90):
    h = {"Content-Type": "application/json"}
    h.update(headers or {})
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, method=method, data=data, headers=h)
    start = time.monotonic()
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            raw = r.read()
            return r.status, dict(r.headers), json.loads(raw), (time.monotonic() - start) * 1000
    except urllib.error.HTTPError as e:
        return e.code, dict(e.headers), {"error": e.read().decode(errors="replace")}, (time.monotonic() - start) * 1000


def argmax_index(q, a):
    """The most probable legend index of a score answer, when it has probabilities."""
    probs = (a or {}).get("probabilities") or {}
    best = None
    for k, v in probs.items():
        try:
            i = int(k)
        except ValueError:
            continue
        if best is None or v > best[1]:
            best = (i, v)
    return None if best is None else best[0]


def answer_of(q, a):
    """Normalizes a raw System One answer: (answer, confidence, raw)."""
    if a is None:
        return None, None, None
    t = q["type"]
    if t == "noul":
        p = a.get("noul")
        if p is None:
            return None, None, None
        return p >= 0.5, max(p, 1 - p), p
    if t == "choice":
        return a.get("choice"), a.get("confidence"), a.get("choice")
    s = a.get("score")
    if s is None:
        return None, None, None
    i = max(0, min(len(q["criteria"]) - 1, int(math.floor(s + 0.5))))
    return i, a.get("confidence"), s


def mirror_answer_of(q, m):
    """A mirror question (answer string + confidence) in the same shape."""
    if m is None or m.get("answer") in (None, ""):
        return None, None, None
    ans, conf = m["answer"], m.get("confidence")
    if q["type"] == "noul":
        return ans == "yes", conf, ans
    if q["type"] == "choice":
        return ans, conf, ans
    labels = q["criteria"]
    if ans in labels:
        return labels.index(ans), conf, ans
    try:
        return int(ans), conf, ans
    except ValueError:
        return None, conf, ans


def correct(expected, got):
    if expected is None or got is None:
        return None
    if isinstance(expected, list):
        return expected[0] <= got <= expected[1]
    return expected == got


def pct(values, p):
    if not values:
        return None
    v = sorted(values)
    k = (len(v) - 1) * p
    lo, hi = math.floor(k), math.ceil(k)
    return round(v[lo] + (v[hi] - v[lo]) * (k - lo))


def mean(values):
    values = [v for v in values if v is not None]
    return round(sum(values) / len(values), 3) if values else None


def ask(gw, model, cat, state, conv):
    body = {"model": model, "state": state, "questions": cat["questions"]}
    st, h, out, ms = http("POST", gw + "/v1/systemone", body, {"Conversation-Id": conv})
    rid = h.get("X-Rlcd-Request-Id") or h.get("x-rlcd-request-id")
    return st, rid, out, ms


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--gateway", default="http://127.0.0.1:4777")
    ap.add_argument("--primary", default="Open-RLCD-text")
    ap.add_argument("--secondary", default="jev-latest")
    ap.add_argument("--mode", choices=["mirror", "both", "primary"], default="mirror")
    ap.add_argument("--pace-ms", type=int, default=0, help="pause between cases")
    ap.add_argument("--dataset", default=os.path.join(HERE, "dataset.json"))
    ap.add_argument("--out", default="")
    ap.add_argument("--only", default="", help="comma-separated case ids (default: all)")
    args = ap.parse_args()

    ds = json.load(open(args.dataset))
    cats = ds["categories"]
    cases = ds["cases"]
    if args.only:
        keep = set(args.only.split(","))
        cases = [c for c in cases if c["id"] in keep]

    rows = []
    for c in cases:
        cat = cats[c["category"]]
        st, rid, out, _ = ask(args.gateway, args.primary, cat, c["state"], "parity-" + c["id"])
        row = {"id": c["id"], "category": c["category"], "primary_request_id": rid, "primary_status": st,
               "primary_raw": out, "secondary_raw": None}
        if args.mode == "both":
            st2, rid2, out2, _ = ask(args.gateway, args.secondary, cat, c["state"], "parity-" + c["id"])
            row.update(secondary_request_id=rid2, secondary_status=st2, secondary_raw=out2)
        rows.append(row)
        print(f"{c['id']:10} primary {st}", file=sys.stderr)
        if args.pace_ms:
            time.sleep(args.pace_ms / 1000)

    # Read the gateway's records: timings, and the mirror's answers.
    want = {r["primary_request_id"] for r in rows} | {r.get("secondary_request_id") for r in rows}
    want.discard(None)
    records = {}
    deadline = time.time() + 120
    while time.time() < deadline:
        st, _, out, _ = http("GET", args.gateway + "/api/decisions?limit=500")
        for it in out.get("items", []) if isinstance(out, dict) else []:
            if it["request_id"] in want:
                records[it["request_id"]] = it
        mirrored = all(records.get(r["primary_request_id"], {}).get("mirror") for r in rows)
        if len(records) >= len(want) and (args.mode != "mirror" or mirrored):
            break
        time.sleep(2)

    results = []
    for r in rows:
        c = next(x for x in cases if x["id"] == r["id"])
        cat = cats[r["category"]]
        prec = records.get(r["primary_request_id"], {})
        mirror = prec.get("mirror")
        srec = records.get(r.get("secondary_request_id"), {}) if args.mode == "both" else {}
        p_ms = prec.get("duration_ms")
        s_ms = srec.get("duration_ms") if args.mode == "both" else (mirror or {}).get("duration_ms")
        for qid, q in cat["questions"].items():
            pa = (r["primary_raw"] or {}).get("answers", {}).get(qid) if isinstance(r["primary_raw"], dict) else None
            p_ans, p_conf, p_raw = answer_of(q, pa)
            if args.mode == "primary":
                s_ans, s_conf, s_raw = None, None, None
            elif args.mode == "both":
                sa = (r["secondary_raw"] or {}).get("answers", {}).get(qid) if isinstance(r["secondary_raw"], dict) else None
                s_ans, s_conf, s_raw = answer_of(q, sa)
            else:
                mq = next((m for m in (mirror or {}).get("questions", []) if m["id"] == qid), None)
                s_ans, s_conf, s_raw = mirror_answer_of(q, mq)
            exp = c["expected"].get(qid)
            p_argmax = argmax_index(q, pa) if q["type"] == "score" else None
            results.append({"case": r["id"], "category": r["category"], "question": qid, "type": q["type"],
                            "expected": exp, "primary": p_ans, "primary_raw": p_raw, "primary_conf": p_conf,
                            "secondary": s_ans, "secondary_raw": s_raw, "secondary_conf": s_conf,
                            "primary_probabilities": (pa or {}).get("probabilities"), "primary_argmax": p_argmax,
                            "primary_argmax_correct": correct(exp, p_argmax) if q["type"] == "score" else None,
                            "agree": None if p_ans is None or s_ans is None else p_ans == s_ans,
                            "primary_correct": correct(exp, p_ans), "secondary_correct": correct(exp, s_ans),
                            "primary_ms": p_ms, "secondary_ms": s_ms,
                            "primary_request_id": r["primary_request_id"],
                            "secondary_error": ((mirror or {}).get("error") or ("" if mirror else "no mirror result (skipped?)"))
                            if args.mode == "mirror" else None})

    summary = []
    for cid, cat in cats.items():
        rs = [x for x in results if x["category"] == cid]
        if not rs:
            continue
        calls = {x["case"]: x for x in rs}.values()
        pc = [x["primary_correct"] for x in rs if x["primary_correct"] is not None]
        sc = [x["secondary_correct"] for x in rs if x["secondary_correct"] is not None]
        ag = [x["agree"] for x in rs if x["agree"] is not None]
        am = [x["primary_argmax_correct"] for x in rs if x["primary_argmax_correct"] is not None]
        summary.append({
            "category": cid, "title": cat["title"], "cases": len(calls), "answers": len(rs),
            "agreement": f"{sum(ag)}/{len(ag)}", "agreement_rate": round(sum(ag) / len(ag), 3) if ag else None,
            "with_expected": len(pc),
            "primary_accuracy": f"{sum(pc)}/{len(pc)}", "secondary_accuracy": f"{sum(sc)}/{len(sc)}",
            "primary_accuracy_score_argmax": f"{sum(am)}/{len(am)}" if am else None,
            "primary_mean_conf": mean([x["primary_conf"] for x in rs]),
            "secondary_mean_conf": mean([x["secondary_conf"] for x in rs]),
            "primary_p50_ms": pct([x["primary_ms"] for x in calls if x["primary_ms"] is not None], 0.5),
            "primary_p95_ms": pct([x["primary_ms"] for x in calls if x["primary_ms"] is not None], 0.95),
            "secondary_p50_ms": pct([x["secondary_ms"] for x in calls if x["secondary_ms"] is not None], 0.5),
            "secondary_p95_ms": pct([x["secondary_ms"] for x in calls if x["secondary_ms"] is not None], 0.95),
        })
    allp = [x["primary_correct"] for x in results if x["primary_correct"] is not None]
    alls = [x["secondary_correct"] for x in results if x["secondary_correct"] is not None]
    alla = [x["agree"] for x in results if x["agree"] is not None]
    total = {"cases": len(rows), "answers": len(results), "agreement": f"{sum(alla)}/{len(alla)}",
             "primary_accuracy": f"{sum(allp)}/{len(allp)}", "secondary_accuracy": f"{sum(alls)}/{len(alls)}"}

    report = {"primary": args.primary, "secondary": args.secondary, "mode": args.mode,
              "run_at": time.strftime("%Y-%m-%dT%H:%M:%S%z"), "summary": summary, "total": total, "results": results}
    if args.out:
        with open(args.out, "w") as f:
            json.dump(report, f, indent=1)
            f.write("\n")

    p, s = args.primary, args.secondary
    print(f"| Category | Cases (answers) | Agreement | Accuracy {p} | Accuracy {s} | Mean conf {p} | Mean conf {s} | "
          f"p50/p95 ms {p} | p50/p95 ms {s} |")
    print("|---|---|---|---|---|---|---|---|---|")
    for x in summary:
        print(f"| {x['title']} | {x['cases']} ({x['answers']}) | {x['agreement']} | {x['primary_accuracy']} | "
              f"{x['secondary_accuracy']} | {x['primary_mean_conf']} | {x['secondary_mean_conf']} | "
              f"{x['primary_p50_ms']}/{x['primary_p95_ms']} | {x['secondary_p50_ms']}/{x['secondary_p95_ms']} |")
    print(f"| **Total** | {total['cases']} ({total['answers']}) | {total['agreement']} | {total['primary_accuracy']} | "
          f"{total['secondary_accuracy']} | | | | |")
    for x in summary:
        if x["primary_accuracy_score_argmax"]:
            print(f"\n{x['title']}: {p} score questions scored by the most probable index instead of the rounded "
                  f"expected score: {x['primary_accuracy_score_argmax']}")
    print("\nDisagreements and misses:")
    for x in results:
        if x["agree"] is False or x["primary_correct"] is False or x["secondary_correct"] is False:
            print(f"  {x['case']:9} {x['question']:12} expected={x['expected']!s:8} {p}={x['primary']!s:9} "
                  f"({x['primary_conf'] or 0:.2f})  {s}={x['secondary']!s:9} ({x['secondary_conf'] or 0:.2f})")


if __name__ == "__main__":
    main()
