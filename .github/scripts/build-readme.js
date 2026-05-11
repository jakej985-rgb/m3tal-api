import fs from "fs";

function readJSON(path) {
  return fs.existsSync(path) ? JSON.parse(fs.readFileSync(path)) : {};
}

const component = readJSON("docs/component.json");

let md = `# ${component.name}\n\n`;

md += `> **Role**: ${component.role}\n`;
md += `> **Technology**: ${component.tech}\n\n`;

md += `## Overview\n`;
md += `This repository contains the Go-native backend for the M3TAL ecosystem. It handles system observability, metrics aggregation, and core control logic.\n\n`;

md += `## Features\n`;
md += `- Native Go performance\n`;
md += `- System state API\n`;
md += `- Real-time metrics processing\n`;
if (component.hasDocker) md += `- Containerized deployment\n`;

md += `\n## M3TAL Ecosystem\n`;
md += `This is a sub-component of the [M3TAL Media Server](https://github.com/jakej985-rgb/M3tal-Media-Server).\n`;
md += `- **Core Orchestrator**: M3tal-Media-Server\n`;
md += `- **Frontend**: m3tal-godash\n`;

fs.writeFileSync("README.generated.md", md);
console.log("README.generated.md assembled for Go Backend.");
