// "Copy as" code for the trace and the Code tab. Keep in step with the
// SDK READMEs: these are the same calls, filled in with sandbox values.

const q = (s) => JSON.stringify(s);

function goValue(v, ind = "") {
  if (v === null || v === undefined) return "nil";
  if (Array.isArray(v)) return `[]any{${v.map((x) => goValue(x, ind)).join(", ")}}`;
  if (typeof v === "object") {
    const ks = Object.keys(v);
    if (!ks.length) return "nil";
    return `map[string]any{${ks.map((k) => `${q(k)}: ${goValue(v[k], ind)}`).join(", ")}}`;
  }
  return JSON.stringify(v);
}

function rsValue(v) {
  if (v === null || v === undefined) return "Value::Null";
  if (Array.isArray(v)) return `Value::Array(vec![${v.map(rsValue).join(", ")}])`;
  if (typeof v === "object") {
    const ks = Object.keys(v);
    if (!ks.length) return "Value::object([])";
    return `Value::object([${ks.map((k) => `(${q(k)}, ${rsValue(v[k])})`).join(", ")}])`;
  }
  if (typeof v === "string") return `${q(v)}.into()`;
  if (typeof v === "boolean") return `Value::Bool(${v})`;
  return `Value::Number(${v}.0)`.replace(/(\.\d+)\.0\)$/, "$1)");
}

function connectJs(v) {
  const auth = v.auth === "private_key_jwt"
    ? `  privateKey: process.env.EHR_PRIVATE_KEY, // PKCS#8 PEM, RS384\n  kid: process.env.EHR_KEY_ID,`
    : `  clientSecret: process.env.EHR_CLIENT_SECRET,`;
  return `import { connect, createSession, runAgent, openAICompatible, jsonTelemetry } from "acyclic-sdk";

const ehr = await connect({
  vendor: ${q(v.id)},
  baseUrl: ${q(v.baseUrl)},
  tokenUrl: ${q(v.tokenUrl)},
  clientId: process.env.EHR_CLIENT_ID,
${auth}${v.practiceId ? `\n  practiceId: ${q(v.practiceId)},` : ""}
  telemetry: jsonTelemetry(),
});`;
}

function connectGo(v) {
  const auth = v.auth === "private_key_jwt"
    ? `		PrivateKeyPEM: os.Getenv("EHR_PRIVATE_KEY"), // PKCS#8, RS384\n		KeyID:         os.Getenv("EHR_KEY_ID"),`
    : `		ClientSecret:  os.Getenv("EHR_CLIENT_SECRET"),`;
  return `	ehr, err := acyclic.Connect(ctx, acyclic.Config{
		Vendor:        ${q(v.id)},
		BaseURL:       ${q(v.baseUrl)},
		TokenURL:      ${q(v.tokenUrl)},
		ClientID:      os.Getenv("EHR_CLIENT_ID"),
${auth}${v.practiceId ? `\n		PracticeID:    ${q(v.practiceId)},` : ""}
		Telemetry:     acyclic.JSONTelemetry(os.Stderr),
	})
	if err != nil {
		log.Fatal(err)
	}`;
}

function connectRs(v) {
  const auth = v.auth === "private_key_jwt"
    ? `        auth: Auth::PrivateKeyJwt { key_pem: env("EHR_PRIVATE_KEY"), kid: Some(env("EHR_KEY_ID")) },`
    : `        auth: Auth::ClientSecret(env("EHR_CLIENT_SECRET")),`;
  return `    let ehr = Ehr::connect(Config {
        vendor: ${q(v.id)}.into(),
        base_url: ${q(v.baseUrl)}.into(),
        token_url: Some(${q(v.tokenUrl)}.into()),
        client_id: env("EHR_CLIENT_ID"),
${auth}${v.practiceId ? `\n        practice_id: Some(${q(v.practiceId)}.into()),` : ""}
        ..Config::default()
    }, UreqTransport::default())?;`;
}

export const snippets = {
  toolCall(lang, name, args, v) {
    if (lang === "go") return `res := sess.Call(ctx, ${q(name)}, ${goValue(args)})\nfmt.Println(res.JSON())`;
    if (lang === "rs") return `let res = session.call(${q(name)}, ${rsValue(args)})?;\nprintln!("{}", res);`;
    return `const res = await session.call(${q(name)}, ${JSON.stringify(args)});\nconsole.log(res.ok ? res.result : res.error);`;
  },

  curl(e, v) {
    const tok = v.auth === "private_key_jwt"
      ? `# 1. Get a token: POST a signed RS384 client assertion (see the SDK README).\nTOKEN=$(curl -s ${q(v.tokenUrl)} \\\n  -d grant_type=client_credentials \\\n  -d client_assertion_type=urn:ietf:params:oauth:client-assertion-type:jwt-bearer \\\n  -d client_assertion="$ASSERTION" | jq -r .access_token)`
      : `# 1. Get a token with your client id and secret.\nTOKEN=$(curl -s -u "$EHR_CLIENT_ID:$EHR_CLIENT_SECRET" ${q(v.tokenUrl)} \\\n  -d grant_type=client_credentials | jq -r .access_token)`;
    const body = e.requestBody ? ` \\\n  -H 'Content-Type: application/fhir+json' \\\n  -d '${JSON.stringify(e.requestBody).replace(/'/g, "'\\''")}'` : "";
    return `${tok}\n\n# 2. The request.\ncurl -s -X ${e.method} '${e.url}' \\\n  -H "Authorization: Bearer $TOKEN" \\\n  -H 'Accept: application/fhir+json'${body}`;
  },

  program(lang, v, patient) {
    const p = patient ?? "PATIENT_ID";
    if (lang === "go") return `package main

import (
	"context"
	"fmt"
	"log"
	"os"

	acyclic "github.com/GrahamGoudeau/acyclic-sdk-go"
)

func main() {
	ctx := context.Background()
${connectGo(v)}

	sess := acyclic.NewSession(ehr, acyclic.SessionOptions{Patient: ${q(p)}})
	model := acyclic.OpenAICompatible(acyclic.OpenAIConfig{
		BaseURL: "https://openrouter.ai/api/v1",
		APIKey:  os.Getenv("OPENROUTER_API_KEY"),
		Model:   "deepseek/deepseek-v4.1-flash-1017",
	})
	out, err := acyclic.RunAgent(ctx, acyclic.AgentOptions{
		EHR: ehr, Session: sess, Model: model,
		DataPolicy: acyclic.Synthetic, // sandbox hosts only; use acyclic.BAA for real data
		Prompt:     "Summarize this patient's active problems and medications.",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(out.Text)
}
`;
    if (lang === "rs") return `use acyclic_sdk::{Auth, Config, Ehr, Session, SessionOptions, UreqTransport, agent::{run_agent, AgentOptions, DataPolicy}, models::OpenAiCompatible};

fn env(k: &str) -> String { std::env::var(k).unwrap_or_default() }

fn main() -> Result<(), acyclic_sdk::Error> {
${connectRs(v)}

    let mut session = Session::new(&ehr, SessionOptions { patient: Some(${q(p)}.into()), ..Default::default() });
    let model = OpenAiCompatible::new("https://openrouter.ai/api/v1", &env("OPENROUTER_API_KEY"), "deepseek/deepseek-v4.1-flash-1017", UreqTransport::default());
    let out = run_agent(&mut session, &model, AgentOptions {
        prompt: "Summarize this patient's active problems and medications.".into(),
        data_policy: DataPolicy::Synthetic, // sandbox hosts only; DataPolicy::Baa for real data
        ..Default::default()
    })?;
    println!("{}", out.text);
    Ok(())
}
`;
    if (lang === "curl") return snippets.curl({ method: "GET", url: `${v.baseUrl}/Patient/${p}` }, v);
    return `${connectJs(v)}

const session = createSession(ehr, { patient: ${q(p)} });
const model = openAICompatible({
  baseUrl: "https://openrouter.ai/api/v1",
  apiKey: process.env.OPENROUTER_API_KEY,
  model: "deepseek/deepseek-v4.1-flash-1017",
});

const out = await runAgent({
  ehr, session, model,
  dataPolicy: "synthetic", // sandbox hosts only; "baa" for real patient data
  prompt: "Summarize this patient's active problems and medications.",
});
console.log(out.text);
`;
  },
};
