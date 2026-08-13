import argparse
import json


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--input", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()

    with open(args.input, "r", encoding="utf-8") as source:
        audit_input = json.load(source)

    summary = audit_input["summary"]
    numeric = summary.get("numeric") or {}
    count = int(numeric.get("count", 0))
    deviation = float(numeric.get("standardDeviation", 0))
    verdict = "stable" if count >= 10 and deviation <= 10 else "not_stable"
    confidence = min(0.9, 0.45 + count / 100) if count else 0.1
    result = {
        "schema": "audit.mod-analysis-result.v1",
        "verdict": verdict,
        "confidence": confidence,
        "summary": f"共解析 {count} 个数字，标准差为 {deviation:.3f}。",
        "evidence": [f"均值：{numeric.get('mean', 0):.3f}", f"P50：{numeric.get('p50', 0):.3f}"],
        "warnings": ["数字稳定性不能单独证明模型身份。"],
        "metrics": {"count": count, "standardDeviation": deviation},
    }
    with open(args.output, "w", encoding="utf-8") as destination:
        json.dump(result, destination, ensure_ascii=False, indent=2)


if __name__ == "__main__":
    main()
