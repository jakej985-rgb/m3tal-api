import fs from "fs";

if (!fs.existsSync("docs")) {
  fs.mkdirSync("docs");
}

const component = {
  name: "m3tal-goback",
  role: "API / System Intelligence",
  tech: "Go",
  hasDocker: fs.existsSync("Dockerfile"),
  hasTests: fs.existsSync("main_test.go") || fs.existsSync("tests"),
};

fs.writeFileSync("docs/component.json", JSON.stringify(component, null, 2));
console.log("Go Backend component state parsed.");
