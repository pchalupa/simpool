#!/usr/bin/env python3
"""Print the scenario prompt with {{capture.path}} references resolved.

A scenario can only talk about a lease or device that the seed actually created,
so the prompt is rendered after seeding, from the facts the seed captured.
"""

import json
import os
import sys

from seed import resolve

scenario = json.load(open(sys.argv[1]))
facts_path = os.path.join(os.environ["SANDBOX"], "logs", "seed-facts.json")
facts = json.load(open(facts_path)) if os.path.exists(facts_path) else {}

sys.stdout.write(resolve(scenario["prompt"], facts))
