"""
Decode VM D entries using the algorithm from zhihu_sign.js case 350.
"""
import json
from pathlib import Path

with open(Path(__file__).parent / 'vm_state_full.json') as f:
    vm = json.load(f)

D = vm['bytecode']['D']
print(f"D entries: {len(D)}\n")

def decode(s: str) -> str:
    u = 66
    result = []
    for ch in s:
        decoded = 24 ^ ord(ch) ^ u
        result.append(chr(decoded))
        u = decoded
    return ''.join(result)

decoded = []
for idx, entry in enumerate(D):
    raw = decode(entry)
    parts = raw.split('|')
    type_val = int(parts[0])
    rest = '|'.join(parts[1:])

    if type_val == 0:
        value = rest
    elif type_val == 1:
        value = float(rest) if '.' in rest else int(rest)
    elif type_val == 2:
        value = f"eval:{rest}"
    elif type_val == 3:
        value = None
    else:
        value = f"type_{type_val}:{rest}"

    decoded.append({
        'idx': idx,
        'raw': raw,
        'type': type_val,
        'value': value,
    })

# Print all
print("=== All D Entries ===")
for d in decoded:
    print(f"D[{d['idx']:3d}]: type={d['type']} raw=\"{d['raw'][:80]}\" value={d['value']!r}")

# Statistics
print("\n=== Type Distribution ===")
from collections import Counter
by_type = Counter(d['type'] for d in decoded)
for t, c in sorted(by_type.items()):
    print(f"  type {t}: {c} entries")

# Show strings (type 0)
print("\n=== String Entries (type 0) ===")
for d in decoded:
    if d['type'] == 0:
        print(f"  D[{d['idx']:3d}]: {d['value']!r}")

# Show numbers (type 1)
print("\n=== Number Entries (type 1) ===")
for d in decoded:
    if d['type'] == 1:
        print(f"  D[{d['idx']:3d}]: {d['value']}")

# Show eval (type 2)
print("\n=== Eval Entries (type 2) ===")
for d in decoded:
    if d['type'] == 2:
        print(f"  D[{d['idx']:3d}]: {d['value']}")

# Show other types
print("\n=== Other Type Entries ===")
for d in decoded:
    if d['type'] > 3:
        print(f"  D[{d['idx']:3d}]: type={d['type']} value={d['value']!r}")
