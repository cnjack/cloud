//! jcloud-mobile — the Tauri shell. All app logic lives in the webview
//! (../src on @jcloud/device-ui); the Rust side adds the two things a webview
//! cannot do for this app:
//!
//!   1. CORS-free HTTP. The app calls the orchestrator cross-origin
//!      (http(s)://<cloud>/api/v1 from origin http://tauri.localhost), and
//!      WebView fetch/XHR/EventSource enforce CORS like any browser. The
//!      `device_fetch` command below therefore performs plain request/response
//!      HTTP natively (reqwest); main.tsx patches window.fetch over it.
//!   2. Streaming SSE. The device event stream (GET /devices/{id}/stream,
//!      ?access_token= in the URL) runs through `device_stream_start`, which
//!      pumps parsed SSE frames back over a tauri::ipc::Channel. main.tsx
//!      patches that over window.EventSource.

use futures_util::future::{abortable, AbortHandle};
use futures_util::StreamExt;
use std::collections::HashMap;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex};
use tauri::ipc::Channel;
use tauri::State;

/// One plain request/response round-trip, mirrored from the JS fetch shim.
#[derive(serde::Deserialize)]
struct FetchRequest {
    method: String,
    url: String,
    #[serde(default)]
    headers: HashMap<String, String>,
    body: Option<String>,
}

#[derive(serde::Serialize)]
struct FetchResponse {
    status: u16,
    headers: HashMap<String, String>,
    body: String,
}

#[tauri::command]
async fn device_fetch(req: FetchRequest) -> Result<FetchResponse, String> {
    let method = reqwest::Method::from_bytes(req.method.as_bytes()).map_err(|e| e.to_string())?;
    let mut rb = reqwest::Client::new().request(method, &req.url);
    for (k, v) in req.headers {
        rb = rb.header(k, v);
    }
    if let Some(body) = req.body {
        rb = rb.body(body);
    }
    let resp = rb.send().await.map_err(|e| e.to_string())?;
    let status = resp.status().as_u16();
    let headers = resp
        .headers()
        .iter()
        .filter_map(|(k, v)| v.to_str().ok().map(|s| (k.as_str().to_string(), s.to_string())))
        .collect();
    let body = resp.text().await.map_err(|e| e.to_string())?;
    Ok(FetchResponse { status, headers, body })
}

/// One message from the Rust SSE pump to the JS EventSource shim.
#[derive(Clone, serde::Serialize)]
#[serde(tag = "kind", rename_all = "lowercase")]
enum SseMsg {
    Open,
    Frame { event: String, data: String },
    Error { message: String },
}

/// Abort handles for live streams, keyed by stream id.
#[derive(Default, Clone)]
struct StreamRegistry(Arc<Mutex<HashMap<String, AbortHandle>>>);

static NEXT_STREAM_ID: AtomicU64 = AtomicU64::new(1);

/// Parse + forward an SSE response until it ends, errors, or is aborted.
async fn run_stream(url: &str, on_msg: &Channel<SseMsg>) -> Result<(), String> {
    let client = reqwest::Client::new();
    let resp = client
        .get(url)
        .header("Accept", "text/event-stream")
        .send()
        .await
        .map_err(|e| e.to_string())?;
    if !resp.status().is_success() {
        return Err(format!("HTTP {}", resp.status()));
    }
    on_msg.send(SseMsg::Open).map_err(|e| e.to_string())?;

    let mut stream = resp.bytes_stream();
    let mut buf = String::new();
    let mut event = String::new();
    let mut data = String::new();
    while let Some(chunk) = stream.next().await {
        let chunk = chunk.map_err(|e| e.to_string())?;
        buf.push_str(&String::from_utf8_lossy(&chunk));
        while let Some(pos) = buf.find('\n') {
            let line = buf[..pos].trim_end_matches('\r').to_string();
            buf.drain(..=pos);
            if line.is_empty() {
                if !data.is_empty() {
                    let name = if event.is_empty() { "message".to_string() } else { event.clone() };
                    let payload = data.trim_end_matches('\n').to_string();
                    on_msg
                        .send(SseMsg::Frame { event: name, data: payload })
                        .map_err(|e| e.to_string())?;
                }
                event.clear();
                data.clear();
            } else if let Some(v) = line.strip_prefix("event:") {
                event = v.trim().to_string();
            } else if let Some(v) = line.strip_prefix("data:") {
                data.push_str(v.strip_prefix(' ').unwrap_or(v));
                data.push('\n');
            }
            // comment/heartbeat lines (": …") and id:/retry: are ignored
        }
    }
    Ok(())
}

#[tauri::command]
async fn device_stream_start(
    registry: State<'_, StreamRegistry>,
    url: String,
    on_msg: Channel<SseMsg>,
) -> Result<String, String> {
    let id = NEXT_STREAM_ID.fetch_add(1, Ordering::Relaxed).to_string();
    let registry = registry.inner().clone();
    let task_id = id.clone();
    let task_registry = registry.clone();
    let (task, abort) = abortable(async move {
        if let Err(message) = run_stream(&url, &on_msg).await {
            let _ = on_msg.send(SseMsg::Error { message });
        }
        if let Ok(mut map) = task_registry.0.lock() {
            map.remove(&task_id);
        }
    });
    registry
        .0
        .lock()
        .map_err(|e| e.to_string())?
        .insert(id.clone(), abort);
    tauri::async_runtime::spawn(task);
    Ok(id)
}

#[tauri::command]
async fn device_stream_close(registry: State<'_, StreamRegistry>, id: String) -> Result<(), String> {
    if let Ok(mut map) = registry.0.lock() {
        if let Some(abort) = map.remove(&id) {
            abort.abort();
        }
    }
    Ok(())
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .manage(StreamRegistry::default())
        .invoke_handler(tauri::generate_handler![
            device_fetch,
            device_stream_start,
            device_stream_close
        ])
        .run(tauri::generate_context!())
        .expect("error while running jcloud-mobile");
}
