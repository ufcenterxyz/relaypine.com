process.env.PORT ||= "4810";
process.env.HOSTNAME ||= "0.0.0.0";
await import(new URL("../.next/standalone/server.js", import.meta.url));
