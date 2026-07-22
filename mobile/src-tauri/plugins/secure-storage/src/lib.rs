use serde::Serialize;
use tauri::{
    plugin::{Builder, TauriPlugin},
    Manager, Runtime,
};

#[cfg(mobile)]
mod mobile;

#[derive(Debug, thiserror::Error)]
pub enum Error {
    #[cfg(mobile)]
    #[error(transparent)]
    PluginInvoke(#[from] tauri::plugin::mobile::PluginInvokeError),
    #[error(transparent)]
    Tauri(#[from] tauri::Error),
}

impl Serialize for Error {
    fn serialize<S>(&self, serializer: S) -> std::result::Result<S::Ok, S::Error>
    where
        S: serde::Serializer,
    {
        serializer.serialize_str(&self.to_string())
    }
}

pub type Result<T> = std::result::Result<T, Error>;

#[cfg(mobile)]
pub use mobile::SecureStorage;

#[cfg(desktop)]
pub struct SecureStorage<R: Runtime> {
    // The desktop fallback does not own a Runtime value. Using a function
    // marker keeps the type parameter without inheriting R's Send/Sync bounds,
    // which tauri::State requires for managed state.
    _runtime: std::marker::PhantomData<fn() -> R>,
    values: std::sync::Mutex<std::collections::HashMap<String, String>>,
}

#[cfg(desktop)]
impl<R: Runtime> SecureStorage<R> {
    pub fn get(&self, key: &str) -> Result<Option<String>> {
        Ok(self.values.lock().unwrap().get(key).cloned())
    }

    pub fn set(&self, key: &str, value: &str) -> Result<()> {
        self.values.lock().unwrap().insert(key.into(), value.into());
        Ok(())
    }

    pub fn delete(&self, key: &str) -> Result<()> {
        self.values.lock().unwrap().remove(key);
        Ok(())
    }
}

pub trait SecureStorageExt<R: Runtime> {
    fn secure_storage(&self) -> tauri::State<'_, SecureStorage<R>>;
}

impl<R: Runtime, T: Manager<R>> SecureStorageExt<R> for T {
    fn secure_storage(&self) -> tauri::State<'_, SecureStorage<R>> {
        self.state::<SecureStorage<R>>()
    }
}

pub fn init<R: Runtime>() -> TauriPlugin<R> {
    Builder::new("jcode-secure-storage")
        .setup(|app, api| {
            #[cfg(mobile)]
            let storage = mobile::init(app, api)?;
            #[cfg(desktop)]
            let _ = api;
            #[cfg(desktop)]
            let storage: SecureStorage<R> = SecureStorage {
                _runtime: std::marker::PhantomData,
                values: Default::default(),
            };
            app.manage(storage);
            Ok(())
        })
        .build()
}
