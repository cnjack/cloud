// Ambient types for CSS Modules imports.
declare module '*.module.css' {
  const classes: { readonly [key: string]: string };
  export default classes;
}
