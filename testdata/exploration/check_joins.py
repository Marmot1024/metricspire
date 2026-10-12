"""Independent multi-table result oracle. SQLite is not Databricks acceptance."""
import datetime as dt
import json
import sqlite3
import sys

payload = json.load(sys.stdin)
mode = payload['mode']
db = sqlite3.connect(':memory:')
def trunc(value, grain):
    day = dt.date.fromisoformat(value)
    return (day - dt.timedelta(days=day.weekday())).isoformat()
def fail(message):
    raise ValueError(message)
db.create_function('trunc', 2, trunc)
db.create_function('weekday', 1, lambda day: dt.date.fromisoformat(day).weekday())
db.create_function('date_add', 2, lambda day,n:(dt.date.fromisoformat(day)+dt.timedelta(days=n)).isoformat())
db.create_function('raise_error', 1, fail)
db.executescript('''
CREATE TABLE orders(order_id TEXT, person_id TEXT, day TEXT, item_id TEXT, amount REAL, valid BOOLEAN, region TEXT);
CREATE TABLE labels(person_id TEXT, day TEXT, tier TEXT, region TEXT);
CREATE TABLE items(item_id TEXT, category_id TEXT);
CREATE TABLE categories(category_id TEXT, category TEXT);
INSERT INTO orders VALUES ('a','p','2026-08-03','i',10,1,'east'),('b','p','2026-08-06','i',20,1,'east'),('c','p','2026-08-10','j',30,1,'east'),('d','missing','2026-08-11','i',40,1,'east'),('ignored','p','2026-08-03','i',999,0,'east');
INSERT INTO labels VALUES ('p','2026-08-03','start1','east'),('p','2026-08-06','mid1','east'),('p','2026-08-09','end1','east'),('p','2026-08-10','start2','east'),('p','2026-08-16','end2','east');
INSERT INTO items VALUES ('i','x'),('j','y');
INSERT INTO categories VALUES ('x','books'),('y','games');
''')
if mode == 'duplicate':
    db.execute("INSERT INTO labels VALUES ('p','2026-08-09','conflicting','east')")
if mode == 'composite':
    db.execute("INSERT INTO labels VALUES ('p','2026-08-09','other_region','west')")
params = {}
for p in payload['statement']['parameters']:
    v = p['value']
    if p['type'] == 'BOOLEAN': v = v == 'true'
    elif p['type'] == 'BIGINT': v = int(v)
    elif p['type'].startswith('DECIMAL'): v = float(v)
    params[p['name']] = v
if mode in ('duplicate','missing_error'):
    try: list(db.execute(payload['statement']['statement'],params))
    except sqlite3.OperationalError: print('rejected ambiguous or missing match'); sys.exit(0)
    raise AssertionError('bad relation returned data')
expected = [('2026-08-03','end1','books',2,30),('2026-08-10',None,'books',1,40),('2026-08-10','end2','games',1,30)]
if mode == 'week_start': expected[0] = ('2026-08-03','start1','books',2,30); expected[2] = ('2026-08-10','start2','games',1,30)
if mode == 'same_day': expected = [('2026-08-03','mid1','books',1,20),('2026-08-03','start1','books',1,10),('2026-08-10',None,'books',1,40),('2026-08-10','start2','games',1,30)]
if mode == 'temporary_condition': expected[0] = ('2026-08-03','end1','books',1,20)
if mode == 'staged_customers': expected = [('2026-08-03','end1','books','repeat',1,30,1),('2026-08-10',None,'books','single',1,40,1),('2026-08-10','end2','games','single',1,30,1)]
actual = list(db.execute(payload['statement']['statement'],params))
assert actual == expected, (actual,expected)
print('verified independent purchase totals, multi-hop dimension and historical labels')
