"""Create a local development config with the actual path to the static React build."""
import json
from pathlib import Path

root = Path(__file__).resolve().parents[1]
config = json.loads((root / "deploy/local.example.json").read_text())
config["web_dir"] = str(root / "web/dist")
target = root / "local-config.json"
with target.open("x") as file:
    json.dump(config, file, indent=2)
    file.write("\n")
print(f"Created {target}. Existing configs are never overwritten.")
