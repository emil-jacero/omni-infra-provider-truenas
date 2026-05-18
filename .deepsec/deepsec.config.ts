import { defineConfig } from "deepsec/config";

export default defineConfig({
  projects: [
    { id: "omni-infra-provider-truenas", root: ".." },
    // <deepsec:projects-insert-above>
  ],
});
