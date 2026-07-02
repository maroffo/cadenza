// ABOUTME: Dashboard recipe CRUD: list/view/add/edit/delete over the Firestore recipe store.
// ABOUTME: Write-time fail-closed validation (foods/units must resolve) + provider cache invalidation.

package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/maroffo/cadenza/internal/recipes"
)

// RecipeAdmin is the dashboard's recipe surface: CRUD on the Firestore store,
// the LIVE book (for macro/allergen derivation + write-time validation), the
// food vocabulary for the input datalist, and cache invalidation so an edit is
// visible to the coach immediately.
type RecipeAdmin interface {
	ListRecipes(ctx context.Context) ([]recipes.Recipe, error)
	GetRecipe(ctx context.Context, id string) (*recipes.Recipe, error)
	SaveRecipe(ctx context.Context, r recipes.Recipe) error
	DeleteRecipe(ctx context.Context, id string) error
	Book(ctx context.Context) (*recipes.Book, error)
	Invalidate()
	FoodIDs() []string
}

// recipeCategorie is the soft vocabulary offered in the form datalist; the
// engine does not restrict categoria, but a consistent set keeps the coach's
// category filter meaningful.
var recipeCategorie = []string{
	"colazione", "primo", "secondo", "contorno", "piatto-unico", "zuppa", "merenda", "dolce",
}

var recipeStagioni = []string{"primavera", "estate", "autunno", "inverno"}

// recipeIDRe constrains new recipe ids to a safe slug (doc key + URL segment).
var recipeIDRe = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

func (s *Server) recipesList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Recipes.ListRecipes(r.Context())
	if err != nil {
		slog.Warn("web: recipes list", "err", err)
	}
	s.render(w, "recipes.html", map[string]any{"Page": "recipes", "Recipes": list})
}

func (s *Server) recipeView(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rec, err := s.Recipes.GetRecipe(r.Context(), id)
	if err != nil {
		s.render(w, "error.html", map[string]any{"Msg": "ricettario non disponibile"})
		return
	}
	if rec == nil {
		w.WriteHeader(http.StatusNotFound)
		s.render(w, "error.html", map[string]any{"Msg": "ricetta non trovata: " + id})
		return
	}
	data := map[string]any{"Page": "recipes", "R": rec}
	if book, err := s.Recipes.Book(r.Context()); err == nil {
		m, al := book.RecipePerServing(*rec)
		data["Allergeni"] = al
		data["Macros"] = map[string]float64{
			"kcal": m.Kcal, "carbo": m.CarbG, "proteine": m.ProteinG,
			"grassi": m.FatG, "fibra": m.FiberG,
		}
	}
	s.render(w, "recipe_view.html", data)
}

// recipeAddForm renders an empty add form.
func (s *Server) recipeAddForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, "recipe_form.html", s.formVM("add", "/app/recipes", recipes.Recipe{}, "", nil))
}

// recipeEditForm renders the edit form prefilled from the stored recipe.
func (s *Server) recipeEditForm(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rec, err := s.Recipes.GetRecipe(r.Context(), id)
	if err != nil {
		s.render(w, "error.html", map[string]any{"Msg": "ricettario non disponibile"})
		return
	}
	if rec == nil {
		w.WriteHeader(http.StatusNotFound)
		s.render(w, "error.html", map[string]any{"Msg": "ricetta non trovata: " + id})
		return
	}
	s.render(w, "recipe_form.html",
		s.formVM("edit", "/app/recipes/"+id, *rec, ingredientsToText(rec.Ingredienti), nil))
}

// recipeCreate validates and stores a NEW recipe (id must be free).
func (s *Server) recipeCreate(w http.ResponseWriter, r *http.Request) {
	rec, rawIng, errs := parseRecipeForm(r)
	if !recipeIDRe.MatchString(rec.ID) {
		errs = append([]string{"id non valido: usa minuscole, cifre e trattini (es. 'pasta-tonno')"}, errs...)
	} else if existing, err := s.Recipes.GetRecipe(r.Context(), rec.ID); err == nil && existing != nil {
		errs = append([]string{"esiste già una ricetta con id " + rec.ID}, errs...)
	}
	errs = append(errs, s.validate(r.Context(), rec)...)
	if len(errs) > 0 {
		w.WriteHeader(http.StatusOK)
		s.render(w, "recipe_form.html", s.formVM("add", "/app/recipes", rec, rawIng, errs))
		return
	}
	if !s.persist(w, r, rec, "creata") {
		return
	}
	http.Redirect(w, r, "/app/recipes/"+rec.ID, http.StatusSeeOther)
}

// recipeUpdate validates and overwrites an existing recipe (id from the path).
func (s *Server) recipeUpdate(w http.ResponseWriter, r *http.Request) {
	rec, rawIng, errs := parseRecipeForm(r)
	rec.ID = r.PathValue("id") // the path is authoritative; the form id is ignored on edit
	errs = append(errs, s.validate(r.Context(), rec)...)
	if len(errs) > 0 {
		w.WriteHeader(http.StatusOK)
		s.render(w, "recipe_form.html", s.formVM("edit", "/app/recipes/"+rec.ID, rec, rawIng, errs))
		return
	}
	if !s.persist(w, r, rec, "modificata") {
		return
	}
	http.Redirect(w, r, "/app/recipes/"+rec.ID, http.StatusSeeOther)
}

// persist saves the recipe, invalidates the coach's cached book so the edit is
// live, and records the audit event. Returns false (after writing an error
// response) when the save itself fails.
func (s *Server) persist(w http.ResponseWriter, r *http.Request, rec recipes.Recipe, action string) bool {
	if err := s.Recipes.SaveRecipe(r.Context(), rec); err != nil {
		slog.Error("web: recipe save", "id", rec.ID, "err", err)
		http.Error(w, "salvataggio non riuscito", http.StatusInternalServerError)
		return false
	}
	s.Recipes.Invalidate()
	if s.Audit != nil {
		if err := s.Audit.RecordWebChange(r.Context(), "recipe", rec.ID, rec.ID+" ("+action+")"); err != nil {
			slog.Warn("web: recipe audit event failed", "err", err)
		}
	}
	return true
}

func (s *Server) recipeDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Recipes.DeleteRecipe(r.Context(), id); err != nil {
		http.Error(w, "eliminazione non riuscita", http.StatusInternalServerError)
		return
	}
	s.Recipes.Invalidate()
	if s.Audit != nil {
		if err := s.Audit.RecordWebChange(r.Context(), "recipe", id, id+" (eliminata)"); err != nil {
			slog.Warn("web: recipe delete audit event failed", "err", err)
		}
	}
	http.Redirect(w, r, "/app/recipes", http.StatusSeeOther)
}

// validate runs the engine's write-time check against the live book (fail-closed
// on unresolved foods/units). A book read failure is surfaced as a soft error so
// we never silently store an unvalidated recipe.
func (s *Server) validate(ctx context.Context, rec recipes.Recipe) []string {
	book, err := s.Recipes.Book(ctx)
	if err != nil {
		return []string{"ricettario non raggiungibile per la validazione, riprova"}
	}
	return book.ValidateRecipe(rec)
}

// formVM builds the template model for the add/edit form, preserving submitted
// values on a re-render and carrying the datalists/hints.
func (s *Server) formVM(mode, action string, rec recipes.Recipe, rawIng string, errs []string) map[string]any {
	if rawIng == "" && len(rec.Ingredienti) > 0 {
		rawIng = ingredientsToText(rec.Ingredienti)
	}
	stag := make(map[string]bool, len(rec.Stagioni))
	for _, sName := range rec.Stagioni {
		stag[sName] = true
	}
	var units []string
	if book, err := s.Recipes.Book(context.Background()); err == nil {
		units = book.Units()
	}
	porzioni := ""
	if rec.Porzioni > 0 {
		porzioni = strconv.FormatFloat(rec.Porzioni, 'f', -1, 64)
	}
	return map[string]any{
		"Page": "recipes", "Mode": mode, "Action": action, "Errors": errs,
		"ID": rec.ID, "Nome": rec.Nome, "Categoria": rec.Categoria,
		"Porzioni": porzioni, "Tag": strings.Join(rec.Tag, ", "),
		"Stagioni": stag, "Personale": rec.Personale, "Fonte": rec.Fonte,
		"Ingredienti": rawIng,
		"FoodIDs":     s.Recipes.FoodIDs(), "Categorie": recipeCategorie,
		"StagioniAll": recipeStagioni, "Units": units,
	}
}

// parseRecipeForm reads a recipe from the POST form. It returns the parsed
// recipe, the raw ingredients text (to re-render on error), and any STRUCTURAL
// problems (bad numbers, malformed ingredient lines); semantic validation
// (unknown foods/units) is layered on by the caller via the engine.
func parseRecipeForm(r *http.Request) (recipes.Recipe, string, []string) {
	var errs []string
	rec := recipes.Recipe{
		ID:        strings.TrimSpace(r.FormValue("id")),
		Nome:      strings.TrimSpace(r.FormValue("nome")),
		Categoria: strings.TrimSpace(r.FormValue("categoria")),
		Fonte:     strings.TrimSpace(r.FormValue("fonte")),
		Personale: r.FormValue("personale") == "on" || r.FormValue("personale") == "true",
	}
	if rec.Nome == "" {
		errs = append(errs, "il nome è obbligatorio")
	}
	if rec.Categoria == "" {
		errs = append(errs, "la categoria è obbligatoria")
	}

	porzioni := strings.TrimSpace(r.FormValue("porzioni"))
	if p, err := strconv.ParseFloat(porzioni, 64); err != nil || p <= 0 {
		errs = append(errs, "porzioni deve essere un numero maggiore di 0")
	} else {
		rec.Porzioni = p
	}

	if tag := strings.TrimSpace(r.FormValue("tag")); tag != "" {
		for _, t := range strings.Split(tag, ",") {
			if t = strings.TrimSpace(t); t != "" {
				rec.Tag = append(rec.Tag, t)
			}
		}
	}
	for _, sName := range recipeStagioni {
		if r.FormValue("stagione_"+sName) != "" {
			rec.Stagioni = append(rec.Stagioni, sName)
		}
	}

	rawIng := r.FormValue("ingredienti")
	ings, ingErrs := parseIngredients(rawIng)
	rec.Ingredienti = ings
	errs = append(errs, ingErrs...)
	return rec, rawIng, errs
}

// parseIngredients parses the textarea, one "food_id | qta | unita" per line
// (unita optional, defaults to grams). Blank lines are skipped.
func parseIngredients(raw string) ([]recipes.Ingredient, []string) {
	var out []recipes.Ingredient
	var errs []string
	for i, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) < 2 {
			errs = append(errs, fmt.Sprintf("riga %d: usa 'food_id | qta | unita'", i+1))
			continue
		}
		food := strings.TrimSpace(parts[0])
		qta, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if err != nil {
			errs = append(errs, fmt.Sprintf("riga %d: quantità non valida %q", i+1, strings.TrimSpace(parts[1])))
			continue
		}
		unita := "g"
		if len(parts) >= 3 && strings.TrimSpace(parts[2]) != "" {
			unita = strings.TrimSpace(parts[2])
		}
		out = append(out, recipes.Ingredient{Food: food, Qta: qta, Unita: unita})
	}
	return out, errs
}

// ingredientsToText renders ingredients back into the textarea line format.
func ingredientsToText(ings []recipes.Ingredient) string {
	var b strings.Builder
	for _, ing := range ings {
		unit := ing.Unita
		if unit == "" {
			unit = "g"
		}
		fmt.Fprintf(&b, "%s | %s | %s\n", ing.Food, strconv.FormatFloat(ing.Qta, 'f', -1, 64), unit)
	}
	return b.String()
}
