"""Normalize shared project/tenant handlers' per-route path parameters.

Swagger annotations describe both route variants on the same handler; a project
parameter applies only to paths that actually contain that parameter.
"""
import json
import re
import copy
from pathlib import Path

# JSON is also valid YAML; keep generated Go registration aligned with the spec.
directory = Path("doc/openapi")
json_path = directory / "Agentic_swagger.json"
spec = json.loads(json_path.read_text())
for path, operations in spec["paths"].items():
    names = set(re.findall(r"\{([^}]+)\}", path))
    for operation in operations.values():
        parameters = operation.get("parameters", [])
        operation["parameters"] = [
            parameter for parameter in parameters
            if parameter.get("in") != "path" or parameter["name"] in names
        ]
        for parameter in operation["parameters"]:
            if parameter.get("in") == "path":
                parameter["required"] = True
text = json.dumps(spec, ensure_ascii=False, indent=4) + "\n"
json_path.write_text(text)
(directory / "Agentic_swagger.yaml").write_text(text)
go_path = directory / "Agentic_docs.go"
source = go_path.read_text()
start = source.index("`", source.index("const docTemplate"))
end = source.index("`", start + 1)
template = copy.deepcopy(spec)
template["info"]["title"] = "{{.Title}}"
template["info"]["version"] = "{{.Version}}"
template["info"]["description"] = "{{escape .Description}}"
template["host"] = "{{.Host}}"
template["basePath"] = "{{.BasePath}}"
template["schemes"] = "{{ marshal .Schemes }}"
template_text = json.dumps(template, ensure_ascii=False, indent=4)
template_text = template_text.replace('"{{ marshal .Schemes }}"', "{{ marshal .Schemes }}")
raw_go = "`" + template_text.replace("`", '` + "`" + `') + "`"
go_path.write_text(source[:start] + raw_go + source[end + 1:])
