// Vite's ?raw suffix imports a file as a string. The repo does not reference
// vite/client (which would declare every asset suffix); this narrower
// declaration covers exactly the fixture imports the isoscene tests use.
declare module "*.html?raw" {
  const text: string;
  export default text;
}
