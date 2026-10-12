"""Local numerical oracle; not Databricks engine acceptance.

Execute the emitted SQL in SQLite, supplying DATE-only trunc/weekday functions.
Expected results are independently computed from sets of synthetic event days.
"""
import datetime as dt
import json
import math
import sqlite3
import sys
from collections import defaultdict

payload = json.load(sys.stdin)
connection = sqlite3.connect(":memory:")


def trunc(value, grain):
    day = dt.date.fromisoformat(value)
    if grain == "WEEK":
        day -= dt.timedelta(days=day.weekday())
    elif grain == "MONTH":
        day = day.replace(day=1)
    else:
        raise ValueError(grain)
    return day.isoformat()


connection.create_function("trunc", 2, trunc)
connection.create_function("weekday", 1, lambda value: dt.date.fromisoformat(value).weekday())
connection.execute("CREATE TABLE events(event_id TEXT, person_id TEXT, event_date TEXT, tier TEXT, event_kind TEXT)")
patterns = [[0], [4, 5], [0, 1, 4], [0, 1, 2, 3], [0, 1, 2, 4, 6],
            list(range(7)), [4, 5, 6], [0, 6], [0, 1, 2, 3, 4, 5], [0, 1, 2, 3, 4]]
rows = []
expected_groups = defaultdict(list)
summary = payload["query"]["summarize"]
partitions = summary["calculations"][0]["expression"].get("partition_by", [])
totals = defaultdict(int)
for week in range(2):
    monday = dt.date(2026, 8, 3) + dt.timedelta(days=week * 7)
    for person, offsets in enumerate(patterns):
        tier = "alpha" if person % 2 == 0 else "beta"
        # Duplicate each event to catch accidental COUNT(event) as active days.
        for offset in offsets:
            day = (monday + dt.timedelta(days=offset)).isoformat()
            for repeat in range(2):
                rows.append((f"{week}-{person}-{offset}-{repeat}", str(person), day, tier, "active"))
        # These rows are outside the anchor's approved COUNT(field) universe.
        rows.append((None, str(person), monday.isoformat(), tier, "active"))
        rows.append(("ignored", str(person), (monday + dt.timedelta(days=6)).isoformat(), tier, "ignored"))
        weekdays = sum(offset < 4 for offset in offsets)
        weekends = len(offsets) - weekdays
        band = "low" if len(offsets) <= 2 else "medium" if len(offsets) <= 4 else "high"
        gap = weekdays / 4 - weekends / 3
        preference = "weekday" if gap >= .2 else "weekend" if gap <= -.2 else "balanced"
        values = dict(day=monday.isoformat(), tier=tier, band=band, preference=preference)
        key = tuple(values[name] for name in summary["group_by"])
        expected_groups[key].append((len(offsets), weekdays, weekends))
        totals[tuple(values[name] for name in partitions)] += 1
rows += [("before", "outside", "2026-08-02", "alpha", "active"),
         ("after", "outside", "2026-08-17", "alpha", "active")]
connection.executemany("INSERT INTO events VALUES (?, ?, ?, ?, ?)", rows)
parameters = {}
for p in payload["statement"]["parameters"]:
    value = p["value"]
    if p["type"] == "BIGINT":
        value = int(value)
    elif p["type"].startswith("DECIMAL"):
        value = float(value)
    elif p["type"] == "BOOLEAN":
        value = value == "true"
    parameters[p["name"]] = value
expected = []
for key, entries in expected_groups.items():
    values = dict(zip(summary["group_by"], key))
    means = [sum(entry[i] for entry in entries) / len(entries) for i in range(3)]
    denominator = totals[tuple(values[name] for name in partitions)]
    expected.append(list(key) + [len(entries)] + means + [len(entries) / denominator])
expected.sort(key=lambda row: tuple(row[:len(summary["group_by"])]))
actual = list(connection.execute(payload["statement"]["statement"], parameters))
expected = expected[:payload["query"]["limit"]]
assert len(actual) == len(expected), (actual, expected)
for got, want in zip(actual, expected):
    assert len(got) == len(want), (got, want)
    for a, b in zip(got, want):
        if isinstance(b, (int, float)):
            assert math.isclose(a, b, abs_tol=1e-9), (got, want)
        else:
            assert a == b, (got, want)
print(f"verified {len(actual)} result rows against 20 independently computed person-weeks")
