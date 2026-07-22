use serde::{de::DeserializeOwned, Deserialize, Serialize};
use tauri::{
    plugin::{PluginApi, PluginHandle},
    AppHandle, Runtime,
};

#[cfg(target_os = "android")]
const PLUGIN_IDENTIFIER: &str = "net.j_code.mobile.securestorage";

#[cfg(target_os = "ios")]
tauri::ios_plugin_binding!(init_plugin_jcode_secure_storage);

pub fn init<R: Runtime, C: DeserializeOwned>(
    _app: &AppHandle<R>,
    api: PluginApi<R, C>,
) -> crate::Result<SecureStorage<R>> {
    #[cfg(target_os = "android")]
    let handle = api.register_android_plugin(PLUGIN_IDENTIFIER, "SecureStoragePlugin")?;
    #[cfg(target_os = "ios")]
    let handle = api.register_ios_plugin(init_plugin_jcode_secure_storage)?;
    Ok(SecureStorage(handle))
}

pub struct SecureStorage<R: Runtime>(PluginHandle<R>);

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct KeyArgs<'a> {
    key: &'a str,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct SetArgs<'a> {
    key: &'a str,
    value: &'a str,
}

#[derive(Deserialize)]
struct GetResponse {
    value: Option<String>,
}

impl<R: Runtime> SecureStorage<R> {
    pub fn get(&self, key: &str) -> crate::Result<Option<String>> {
        Ok(self
            .0
            .run_mobile_plugin::<GetResponse>("get", KeyArgs { key })?
            .value)
    }

    pub fn set(&self, key: &str, value: &str) -> crate::Result<()> {
        self.0
            .run_mobile_plugin("set", SetArgs { key, value })
            .map_err(Into::into)
    }

    pub fn delete(&self, key: &str) -> crate::Result<()> {
        self.0
            .run_mobile_plugin("delete", KeyArgs { key })
            .map_err(Into::into)
    }
}

